package sip

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Yunisky/megaco-sip-gateway/internal/socketbind"
)

type Handler interface {
	HandleSIP(context.Context, *Message, Responder)
}

type Responder interface {
	Respond(string) error
	SendTo(string, *net.UDPAddr) error
	LocalAddr() net.Addr
}

type Sender interface {
	SendTo(string, *net.UDPAddr) error
	LocalAddr() net.Addr
}

type UDPServer struct {
	addr          string
	device        string
	handler       Handler
	logger        *slog.Logger
	mu            sync.RWMutex
	conn          *net.UDPConn
	cacheMu       sync.Mutex
	responseCache map[string]cachedResponse
	cacheOrder    []string
	inFlight      map[string]time.Time
}

type cachedResponse struct {
	message   string
	expiresAt time.Time
}

const (
	sipResponseCacheTTL = 5 * time.Minute
	sipInFlightTTL      = 32 * time.Second
)

func NewUDPServer(addr, device string, handler Handler, logger *slog.Logger) (*UDPServer, error) {
	return &UDPServer{
		addr:          addr,
		device:        device,
		handler:       handler,
		logger:        logger,
		responseCache: make(map[string]cachedResponse),
		inFlight:      make(map[string]time.Time),
	}, nil
}

func (s *UDPServer) ListenAndServe(ctx context.Context) error {
	conn, err := socketbind.ListenUDP(ctx, s.addr, s.device)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	defer conn.Close()
	defer func() {
		s.mu.Lock()
		if s.conn == conn {
			s.conn = nil
		}
		s.mu.Unlock()
	}()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

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

func (s *UDPServer) SendTo(message string, remote *net.UDPAddr) error {
	s.mu.RLock()
	conn := s.conn
	s.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("SIP UDP server is not listening")
	}
	_, err := conn.WriteToUDP([]byte(message), remote)
	return err
}

func (s *UDPServer) LocalAddr() net.Addr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.conn == nil {
		return nil
	}
	return s.conn.LocalAddr()
}

func (s *UDPServer) handle(ctx context.Context, data []byte, remote *net.UDPAddr) {
	msg, err := Parse(data)
	if err != nil {
		s.logger.Warn("drop invalid sip", "remote", remote, "error", err)
		return
	}
	msg.Source = remote
	s.logger.Debug("sip received", "remote", remote, "start", msg.StartLine)
	s.mu.RLock()
	conn := s.conn
	s.mu.RUnlock()
	if conn == nil {
		return
	}
	cacheKey := ""
	if msg.Status == 0 && !strings.EqualFold(msg.Method, "ACK") {
		cacheKey = sipTransactionKey(msg, remote)
		if cached := s.cachedResponse(cacheKey); cached != "" {
			if _, err := conn.WriteToUDP([]byte(cached), remote); err != nil {
				s.logger.Warn("send cached sip response", "remote", remote, "method", msg.Method, "error", err)
			} else {
				s.logger.Debug("sip duplicate transaction", "remote", remote, "method", msg.Method, "call_id", msg.CallID(), "cseq", msg.Header("CSeq"))
			}
			return
		}
		if !s.beginTransaction(cacheKey) {
			s.logger.Debug("sip transaction already in flight", "remote", remote, "method", msg.Method, "call_id", msg.CallID())
			return
		}
	}
	s.handler.HandleSIP(ctx, msg, &udpResponder{server: s, conn: conn, remote: remote, cacheKey: cacheKey})
}

type udpResponder struct {
	server   *UDPServer
	conn     *net.UDPConn
	remote   *net.UDPAddr
	cacheKey string
}

func (r *udpResponder) Respond(message string) error {
	if r.server != nil {
		r.server.cacheResponse(r.cacheKey, message)
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

func sipTransactionKey(message *Message, remote *net.UDPAddr) string {
	if message == nil || remote == nil || message.Method == "" {
		return ""
	}
	branch := headerParameterValue(message.Header("Via"), "branch")
	return strings.Join([]string{
		remote.String(),
		strings.ToUpper(message.Method),
		message.CallID(),
		message.Header("CSeq"),
		branch,
	}, "|")
}

func headerParameterValue(value, name string) string {
	for _, part := range strings.Split(value, ";") {
		key, parameter, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.EqualFold(key, name) {
			return strings.Trim(parameter, `"`)
		}
	}
	return ""
}

func (s *UDPServer) beginTransaction(key string) bool {
	if key == "" {
		return true
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	now := time.Now()
	if expiresAt, exists := s.inFlight[key]; exists && now.Before(expiresAt) {
		return false
	}
	s.inFlight[key] = now.Add(sipInFlightTTL)
	return true
}

func (s *UDPServer) cachedResponse(key string) string {
	if key == "" {
		return ""
	}
	now := time.Now()
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	entry, exists := s.responseCache[key]
	if !exists {
		return ""
	}
	if now.After(entry.expiresAt) {
		delete(s.responseCache, key)
		s.removeCachedKeyLocked(key)
		return ""
	}
	return entry.message
}

func (s *UDPServer) cacheResponse(key, message string) {
	if key == "" {
		return
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if _, exists := s.responseCache[key]; !exists {
		s.cacheOrder = append(s.cacheOrder, key)
	}
	s.responseCache[key] = cachedResponse{message: message, expiresAt: time.Now().Add(sipResponseCacheTTL)}
	delete(s.inFlight, key)
	const maxCachedTransactions = 8192
	for len(s.cacheOrder) > maxCachedTransactions {
		oldest := s.cacheOrder[0]
		s.cacheOrder = s.cacheOrder[1:]
		delete(s.responseCache, oldest)
	}
}

func (s *UDPServer) removeCachedKeyLocked(key string) {
	for index := 0; index < len(s.cacheOrder); {
		if s.cacheOrder[index] != key {
			index++
			continue
		}
		copy(s.cacheOrder[index:], s.cacheOrder[index+1:])
		s.cacheOrder = s.cacheOrder[:len(s.cacheOrder)-1]
	}
}
