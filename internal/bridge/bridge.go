package bridge

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Yunisky/megaco-sip-gateway/internal/config"
	"github.com/Yunisky/megaco-sip-gateway/internal/h248"
	"github.com/Yunisky/megaco-sip-gateway/internal/sip"
)

type Bridge struct {
	cfg    config.Config
	logger *slog.Logger

	mu                      sync.Mutex
	sipSender               sip.Sender
	callsBySIP              map[string]*Call
	callsByH248             map[string]*Call
	retainedSIPInvites      map[string]*retainedSIPInvite
	sipInviteTransactionTTL time.Duration
	nextRTPPort             int
	nextContextID           uint64
	nextTermID              uint64
	nextH248TID             atomic.Uint64
	line                    lineState
	outboundCall            *Call
	pendingH248             map[string]pendingH248Transaction
}

type lineState struct {
	ready             bool
	readyAt           time.Time
	eventRequestID    string
	initialOnhookSent bool
	responder         h248.Responder
}

type pendingH248Transaction struct {
	callID string
	phase  string
}

// retainedSIPInvite keeps only the client-transaction fields required after
// an H.248 call has already been released. A canceled INVITE can still receive
// a final response for up to Timer D; non-2xx retransmissions require another
// ACK, and a late 2xx must be ACKed and then cleared with BYE.
type retainedSIPInvite struct {
	callID         string
	requestURI     string
	from           string
	inviteVia      string
	inviteBranch   string
	inviteCSeq     int
	peer           *net.UDPAddr
	expiresAt      time.Time
	late2xxBYESent bool
}

const defaultSIPInviteTransactionTTL = 32 * time.Second

type Call struct {
	ID                   string
	Direction            string
	State                string
	CreatedAt            time.Time
	Released             bool
	H248ContextID        string
	H248ContextAllocated bool
	H248PhysicalTerm     string
	H248EphemeralTerm    string
	H248EventRequestID   string
	H248Responder        h248.Responder
	H248RemoteMedia      *net.UDPAddr
	H248RTPPort          int
	H248TelephoneEventPT int
	H248Answered         bool
	H248OnhookSent       bool
	H248OffhookSent      bool
	H248DigitsSent       bool
	H248DigitsScheduled  bool
	SIPCallID            string
	SIPRequestURI        string
	SIPPeer              *net.UDPAddr
	SIPRemoteMedia       *net.UDPAddr
	SIPRTPPort           int
	SIPTelephoneEventPT  int
	SIPFrom              string
	SIPTo                string
	SIPLocalTag          string
	SIPRemoteTag         string
	SIPRemoteContact     string
	SIPInvite            string
	SIPInviteVia         string
	SIPInviteBranch      string
	SIPInviteCSeq        int
	SIPInviteProvisional bool
	SIPInviteFinal       bool
	SIPAnswered          bool
	SIPRemoteReleased    bool
	SIPInitialRequest    *sip.Message
	SIPResponder         sip.Responder
	SIPLastResponse      string
	CallerID             string
	DestinationNumber    string
	Media                *mediaRelay
}

func New(logger *slog.Logger, cfg config.Config) *Bridge {
	seed := uint64(time.Now().UnixNano() & 0x7fffffff)
	b := &Bridge{
		cfg:                     cfg,
		logger:                  logger,
		callsBySIP:              make(map[string]*Call),
		callsByH248:             make(map[string]*Call),
		retainedSIPInvites:      make(map[string]*retainedSIPInvite),
		sipInviteTransactionTTL: defaultSIPInviteTransactionTTL,
		nextRTPPort:             normalizeEvenPort(cfg.Media.PortMin),
		pendingH248:             make(map[string]pendingH248Transaction),
		nextContextID:           seed,
	}
	b.nextH248TID.Store(seed)
	return b
}

func (b *Bridge) SetSIPSender(sender sip.Sender) {
	b.mu.Lock()
	b.sipSender = sender
	b.mu.Unlock()
}

func (b *Bridge) Close() {
	b.mu.Lock()
	media := make([]*mediaRelay, 0, len(b.callsByH248)+len(b.callsBySIP))
	seen := make(map[*Call]struct{})
	for _, call := range b.callsByH248 {
		seen[call] = struct{}{}
		if call.Media != nil {
			media = append(media, call.Media)
		}
		call.Released = true
	}
	for _, call := range b.callsBySIP {
		if _, exists := seen[call]; exists {
			continue
		}
		if call.Media != nil {
			media = append(media, call.Media)
		}
		call.Released = true
	}
	b.retainedSIPInvites = make(map[string]*retainedSIPInvite)
	b.mu.Unlock()
	for _, relay := range media {
		relay.Close()
	}
}

func (b *Bridge) allocateCallIdentifiersLocked() (contextID, ephemeral string, h248Port, sipPort int) {
	b.nextContextID++
	b.nextTermID++
	contextID = strconv.FormatUint(b.nextContextID&0x7fffffff, 10)
	ephemeral = b.cfg.H248.EphemeralTerminationPrefix + strconv.FormatUint(b.nextTermID, 10)
	if b.cfg.H248.EphemeralTerminationPrefix == "" {
		ephemeral = "RTP/" + strconv.FormatUint(b.nextTermID, 10)
	}
	h248Port = b.allocateRTPPortLocked()
	sipPort = b.allocateRTPPortLocked()
	return
}

func (b *Bridge) allocateRTPPortLocked() int {
	port := b.nextRTPPort
	b.nextRTPPort += 2
	if b.nextRTPPort > b.cfg.Media.PortMax {
		b.nextRTPPort = normalizeEvenPort(b.cfg.Media.PortMin)
	}
	return port
}

func (b *Bridge) newH248TransactionID() string {
	return strconv.FormatUint(b.nextH248TID.Add(1), 10)
}

func (b *Bridge) findCallByContext(contextID string) *Call {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.callsByH248[contextID]
}

func (b *Bridge) releaseCall(call *Call) {
	if call == nil {
		return
	}
	b.mu.Lock()
	if call.Released {
		b.mu.Unlock()
		return
	}
	call.Released = true
	call.State = "released"
	delete(b.callsByH248, call.H248ContextID)
	if call.SIPCallID != "" {
		delete(b.callsBySIP, call.SIPCallID)
	}
	if b.outboundCall == call {
		b.outboundCall = nil
	}
	relay := call.Media
	b.mu.Unlock()
	if relay != nil {
		relay.Close()
	}
	b.logger.Info("call released", "id", call.ID, "context", call.H248ContextID, "sip_call_id", call.SIPCallID)
}

func normalizeEvenPort(port int) int {
	if port <= 0 {
		return 20000
	}
	if port%2 != 0 {
		return port + 1
	}
	return port
}

func cloneUDPAddr(address *net.UDPAddr) *net.UDPAddr {
	if address == nil {
		return nil
	}
	copyAddress := *address
	copyAddress.IP = append(net.IP(nil), address.IP...)
	return &copyAddress
}

func callLogID(call *Call) string {
	if call == nil {
		return ""
	}
	if call.SIPCallID != "" {
		return call.SIPCallID
	}
	return fmt.Sprintf("h248-%s", call.H248ContextID)
}

func localContact(cfg config.Config) string {
	address := cfg.SIP.AdvertisedAddress
	if address == "" {
		address = cfg.SIP.Listen
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		host = address
		port = "5060"
	}
	if host == "" || net.ParseIP(host) != nil && net.ParseIP(host).IsUnspecified() {
		host = cfg.Media.RTPIP
	}
	return "sip:gateway@" + net.JoinHostPort(strings.Trim(host, "[]"), port)
}
