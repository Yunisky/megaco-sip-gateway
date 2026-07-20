package h248

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Yunisky/megaco-sip-gateway/internal/socketbind"
)

type Handler interface {
	HandleH248(context.Context, *Message, Responder)
}

type LifecycleHandler interface {
	HandleH248Ready(context.Context, Responder)
	HandleH248Response(context.Context, *Message, Responder)
}

type AvailabilityHandler interface {
	HandleH248Unavailable(context.Context, *net.UDPAddr)
}

type Responder interface {
	Respond(string) error
	SendTo(string, *net.UDPAddr) error
	LocalAddr() net.Addr
	RemoteAddr() *net.UDPAddr
}

type UDPServer struct {
	config              UDPServerConfig
	handler             Handler
	logger              *slog.Logger
	conn                *net.UDPConn
	registrationMu      sync.RWMutex
	serviceChangeTID    string
	serviceChangeRemote string
	serviceChangeReply  chan registrationResult
	activeMGC           string
	lastMGCActivity     time.Time
	registered          atomic.Bool
	cacheMu             sync.Mutex
	replyCache          map[string]string
	cacheOrder          []string
}

type registrationResult struct {
	transactionID string
	accepted      bool
}

type UDPServerConfig struct {
	Address                   string
	Device                    string
	MGC                       string
	BackupMGC                 string
	MID                       string
	Version                   int
	ServiceChangeMethod       string
	ServiceChangeReason       string
	ServiceChangeProfile      string
	ServiceChangeAddress      string
	ServiceChangeRetrySeconds int
	ServiceChangeMaxAttempts  int
	MGCFailureTimeoutSeconds  int
	PhysicalTermination       string
}

func NewUDPServer(config UDPServerConfig, handler Handler, logger *slog.Logger) (*UDPServer, error) {
	return &UDPServer{
		config:             config,
		handler:            handler,
		logger:             logger,
		serviceChangeReply: make(chan registrationResult, 16),
		replyCache:         make(map[string]string),
	}, nil
}

func (s *UDPServer) ListenAndServe(ctx context.Context) error {
	conn, err := socketbind.ListenUDP(ctx, s.config.Address, s.config.Device)
	if err != nil {
		return err
	}
	s.conn = conn
	defer conn.Close()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	if s.config.MGC != "" {
		remotes := make([]*net.UDPAddr, 0, 2)
		for _, address := range []string{s.config.MGC, s.config.BackupMGC} {
			if strings.TrimSpace(address) == "" {
				continue
			}
			remote, err := net.ResolveUDPAddr("udp", address)
			if err != nil {
				return err
			}
			remotes = append(remotes, remote)
		}
		go s.registrationLoop(ctx, conn, remotes)
	}

	buf := make([]byte, 65535)
	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		data := append([]byte(nil), buf[:n]...)
		go s.handle(ctx, data, remote)
	}
}

func (s *UDPServer) handle(ctx context.Context, data []byte, remote *net.UDPAddr) {
	msg, err := Parse(data)
	if err != nil {
		s.logger.Warn("drop invalid h248", "remote", remote, "error", err)
		return
	}
	expectedMGC := s.expectedMGCAddress()
	managedMGC := strings.TrimSpace(s.config.MGC) != ""
	if (expectedMGC != "" && remote.String() != expectedMGC) || (managedMGC && expectedMGC == "") {
		if msg.TransactionType == "Reply" || msg.TransactionType == "Pending" || !auditOnly(msg) {
			s.logger.Warn("drop H.248 message outside active registration", "remote", remote, "expected_mgc", expectedMGC, "transaction", msg.TransactionID)
			return
		}
	}
	s.noteMGCActivity(remote)
	if msg.TransactionType == "Reply" || msg.TransactionType == "Pending" {
		s.registrationMu.RLock()
		registrationTID := s.serviceChangeTID
		registrationRemote := s.serviceChangeRemote
		s.registrationMu.RUnlock()
		responder := &udpResponder{server: s, conn: s.conn, remote: remote}
		if msg.TransactionID == registrationTID && remote.String() == registrationRemote {
			s.logger.Info("h248 service change response",
				"remote", remote,
				"transaction", msg.TransactionID,
				"type", msg.TransactionType,
				"version", msg.Version,
				"mid", msg.MID,
				"profile", msg.ServiceChangeProfile,
				"error_code", msg.ErrorCode,
			)
			if msg.TransactionType == "Reply" {
				select {
				case s.serviceChangeReply <- registrationResult{transactionID: msg.TransactionID, accepted: msg.ErrorCode == ""}:
				default:
				}
			}
			return
		}
		if lifecycle, ok := s.handler.(LifecycleHandler); ok {
			lifecycle.HandleH248Response(ctx, msg, responder)
			return
		}
		s.logger.Debug("h248 unmatched response", "remote", remote, "transaction", msg.TransactionID)
		return
	}
	s.logger.Debug("h248 received", "remote", remote, "transaction", msg.TransactionID, "commands", len(msg.Commands))
	cacheKey := ""
	if msg.TransactionID != "" {
		cacheKey = remote.String() + "/" + msg.TransactionID
	}
	if cached := s.cachedReply(cacheKey); cached != "" {
		if _, err := s.conn.WriteToUDP([]byte(cached), remote); err != nil {
			s.logger.Warn("send cached h248 reply", "remote", remote, "transaction", msg.TransactionID, "error", err)
		} else {
			s.logger.Debug("h248 duplicate transaction", "remote", remote, "transaction", msg.TransactionID)
		}
		return
	}
	s.handler.HandleH248(ctx, msg, &udpResponder{server: s, conn: s.conn, remote: remote, cacheKey: cacheKey})
}

func (s *UDPServer) registrationLoop(ctx context.Context, conn *net.UDPConn, remotes []*net.UDPAddr) {
	if len(remotes) == 0 {
		return
	}
	startIndex := 0
	for {
		registeredAndFailed := false
		for offset := 0; offset < len(remotes); offset++ {
			index := (startIndex + offset) % len(remotes)
			remote := remotes[index]
			if s.serviceChangeLoop(ctx, conn, remote) {
				if !s.waitForMGCFailure(ctx, remote) {
					return
				}
				s.registered.Store(false)
				s.clearActiveMGC(remote)
				s.logger.Error("active H.248 MGC timed out", "remote", remote, "timeout_seconds", s.config.MGCFailureTimeoutSeconds)
				if lifecycle, ok := s.handler.(AvailabilityHandler); ok {
					lifecycle.HandleH248Unavailable(ctx, cloneUDPAddr(remote))
				}
				startIndex = (index + 1) % len(remotes)
				registeredAndFailed = true
				break
			}
			if ctx.Err() != nil {
				return
			}
			s.logger.Warn("H.248 MGC registration candidate failed", "remote", remote, "standby", index > 0)
		}
		if registeredAndFailed {
			continue
		}
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *UDPServer) serviceChangeLoop(ctx context.Context, conn *net.UDPConn, remote *net.UDPAddr) bool {
	if !s.registerTermination(ctx, conn, remote, "ROOT") {
		return false
	}
	if termination := strings.TrimSpace(s.config.PhysicalTermination); termination != "" {
		if !s.registerTermination(ctx, conn, remote, termination) {
			return false
		}
	}
	s.registered.Store(true)
	s.setActiveMGC(remote)
	s.registrationMu.RLock()
	transactionID := s.serviceChangeTID
	s.registrationMu.RUnlock()
	s.clearRegistrationTransaction(transactionID)
	s.logger.Info("h248 registration active", "remote", remote, "transaction", transactionID, "termination", s.config.PhysicalTermination)
	if lifecycle, ok := s.handler.(LifecycleHandler); ok {
		lifecycle.HandleH248Ready(ctx, &udpResponder{server: s, conn: conn, remote: remote})
	}
	return true
}

func (s *UDPServer) clearRegistrationTransaction(transactionID string) {
	s.registrationMu.Lock()
	defer s.registrationMu.Unlock()
	if s.serviceChangeTID == transactionID {
		s.serviceChangeTID = ""
		s.serviceChangeRemote = ""
	}
}

func (s *UDPServer) waitForMGCFailure(ctx context.Context, remote *net.UDPAddr) bool {
	timeout := time.Duration(s.config.MGCFailureTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 360 * time.Second
	}
	interval := timeout / 4
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	if interval > 5*time.Second {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case now := <-ticker.C:
			s.registrationMu.RLock()
			active := s.activeMGC
			lastActivity := s.lastMGCActivity
			s.registrationMu.RUnlock()
			if active != remote.String() {
				return true
			}
			if now.Sub(lastActivity) >= timeout {
				return true
			}
		}
	}
}

func (s *UDPServer) setActiveMGC(remote *net.UDPAddr) {
	s.registrationMu.Lock()
	defer s.registrationMu.Unlock()
	s.activeMGC = remote.String()
	s.lastMGCActivity = time.Now()
}

func (s *UDPServer) clearActiveMGC(remote *net.UDPAddr) {
	s.registrationMu.Lock()
	defer s.registrationMu.Unlock()
	if remote == nil || s.activeMGC == remote.String() {
		s.activeMGC = ""
		s.lastMGCActivity = time.Time{}
	}
}

func (s *UDPServer) expectedMGCAddress() string {
	s.registrationMu.RLock()
	defer s.registrationMu.RUnlock()
	if s.activeMGC == "" {
		return s.serviceChangeRemote
	}
	return s.activeMGC
}

func (s *UDPServer) noteMGCActivity(remote *net.UDPAddr) {
	if remote == nil {
		return
	}
	s.registrationMu.Lock()
	defer s.registrationMu.Unlock()
	if s.activeMGC == remote.String() {
		s.lastMGCActivity = time.Now()
	}
}

func auditOnly(message *Message) bool {
	if message == nil || len(message.Commands) == 0 {
		return false
	}
	for _, command := range message.Commands {
		if command.Name != "AuditValue" && command.Name != "AuditCapabilities" {
			return false
		}
	}
	return true
}

func cloneUDPAddr(address *net.UDPAddr) *net.UDPAddr {
	if address == nil {
		return nil
	}
	copyAddress := *address
	copyAddress.IP = append(net.IP(nil), address.IP...)
	return &copyAddress
}

func (s *UDPServer) registerTermination(ctx context.Context, conn *net.UDPConn, remote *net.UDPAddr, termination string) bool {
	retry := time.Duration(s.config.ServiceChangeRetrySeconds) * time.Second
	if retry <= 0 {
		retry = 5 * time.Second
	}
	address := ""
	profile := ""
	if strings.EqualFold(termination, "ROOT") {
		address = s.config.ServiceChangeAddress
		if address == "" {
			address = advertisedAddress(s.config.Address)
		}
		profile = s.config.ServiceChangeProfile
	}
	transactionID := strconv.FormatInt(time.Now().UnixNano()&0x7fffffff, 10)
	s.registrationMu.Lock()
	s.serviceChangeTID = transactionID
	s.serviceChangeRemote = remote.String()
	s.registrationMu.Unlock()
	message := BuildServiceChange(ServiceChangeOptions{
		Version:       s.config.Version,
		MID:           s.config.MID,
		TransactionID: transactionID,
		Termination:   termination,
		Method:        s.config.ServiceChangeMethod,
		Reason:        s.config.ServiceChangeReason,
		Profile:       profile,
		Address:       address,
	})

	delay := retry
	const maxRetryDelay = 30 * time.Second
	maxAttempts := s.config.ServiceChangeMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if _, err := conn.WriteToUDP([]byte(message), remote); err != nil {
			s.logger.Warn("send h248 service change", "remote", remote, "termination", termination, "attempt", attempt, "error", err)
		} else {
			s.logger.Info("h248 service change sent", "remote", remote, "transaction", transactionID, "termination", termination, "attempt", attempt)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case result := <-s.serviceChangeReply:
			timer.Stop()
			if result.transactionID != transactionID {
				continue
			}
			if !result.accepted {
				s.logger.Error("h248 service change rejected", "remote", remote, "transaction", transactionID, "termination", termination)
			}
			return result.accepted
		case <-timer.C:
			if delay < maxRetryDelay {
				delay *= 2
				if delay > maxRetryDelay {
					delay = maxRetryDelay
				}
			}
		}
	}
	s.logger.Warn("H.248 service change timed out", "remote", remote, "transaction", transactionID, "termination", termination, "attempts", maxAttempts)
	return false
}

func (s *UDPServer) cachedReply(key string) string {
	if key == "/" {
		return ""
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	return s.replyCache[key]
}

func (s *UDPServer) cacheReply(key, message string) {
	if key == "" {
		return
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if _, exists := s.replyCache[key]; !exists {
		s.cacheOrder = append(s.cacheOrder, key)
	}
	s.replyCache[key] = message
	const maxCachedTransactions = 4096
	if len(s.cacheOrder) > maxCachedTransactions {
		oldest := s.cacheOrder[0]
		s.cacheOrder = s.cacheOrder[1:]
		delete(s.replyCache, oldest)
	}
}

func advertisedAddress(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || strings.Trim(host, "0.") == "" {
		return ""
	}
	if net.ParseIP(host) != nil {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

type udpResponder struct {
	server   *UDPServer
	conn     *net.UDPConn
	remote   *net.UDPAddr
	cacheKey string
}

func (r *udpResponder) Respond(message string) error {
	if r.server != nil {
		r.server.cacheReply(r.cacheKey, message)
	}
	_, err := r.conn.WriteToUDP([]byte(message), r.remote)
	return err
}

func (r *udpResponder) SendTo(message string, remote *net.UDPAddr) error {
	_, err := r.conn.WriteToUDP([]byte(message), remote)
	return err
}

func (r *udpResponder) LocalAddr() net.Addr {
	return r.conn.LocalAddr()
}

func (r *udpResponder) RemoteAddr() *net.UDPAddr {
	if r.remote == nil {
		return nil
	}
	copyAddr := *r.remote
	copyAddr.IP = append(net.IP(nil), r.remote.IP...)
	return &copyAddr
}
