package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Yunisky/megaco-sip-gateway/internal/h248"
	"github.com/Yunisky/megaco-sip-gateway/internal/socketbind"
)

var eventRequestRe = regexp.MustCompile(`(?i)\b(?:Events|E)\s*=\s*([0-9]+)\s*\{`)

type options struct {
	MGC          string
	Source       string
	BindDevice   string
	MID          string
	Termination  string
	Mode         string
	EventStyle   string
	LineRestart  bool
	Number       string
	RTPIP        string
	RTPPort      int
	Hold         time.Duration
	OffhookDelay time.Duration
	DigitDelay   time.Duration
	CallHold     time.Duration
	Timeout      time.Duration
}

type callProbe struct {
	options
	conn             *net.UDPConn
	remote           *net.UDPAddr
	nextTID          uint64
	replyCache       map[string]string
	latestRequestID  string
	serviceChangeTID string
	initialOnhookTID string
	initialOnhook    bool
	offhookTID       string
	onhookTID        string
	offhookSent      bool
	digitsTID        string
	digitsSent       bool
	onhookSent       bool
	onhookAt         time.Time
	offhookAt        time.Time
	digitsAt         time.Time
	finishAt         time.Time
	deferLineEvents  bool
	mediaConn        *net.UDPConn
	contextID        string
	ephemeralTerm    string
	incomingSetup    bool
}

func main() {
	var cfg options
	flag.StringVar(&cfg.MGC, "mgc", "127.0.0.1:2944", "active H.248 MGC UDP address")
	flag.StringVar(&cfg.Source, "source", "127.0.0.1:0", "local H.248 UDP address")
	flag.StringVar(&cfg.BindDevice, "bind-device", "", "Linux interface or VRF device")
	flag.StringVar(&cfg.MID, "mid", "mgw-probe", "complete H.248 MG mId")
	flag.StringVar(&cfg.Termination, "termination", "aaln/1", "physical analog-line termination")
	flag.StringVar(&cfg.Mode, "mode", "offhook-only", "test mode: offhook-only, outbound, inbound-observe, or inbound-answer")
	flag.StringVar(&cfg.EventStyle, "event-style", "plain", "ObservedEvent style: plain, paren-init, or brace-init")
	flag.BoolVar(&cfg.LineRestart, "line-service-change", false, "send ServiceChange/Restart for the physical termination after ROOT")
	flag.StringVar(&cfg.Number, "number", "", "authorized destination number for outbound mode")
	flag.StringVar(&cfg.RTPIP, "rtp-ip", "127.0.0.1", "local carrier-side RTP address")
	flag.IntVar(&cfg.RTPPort, "rtp-port", 4000, "local carrier-side RTP port")
	flag.DurationVar(&cfg.Hold, "hold", 3*time.Second, "time to remain off-hook after the post-offhook Modify")
	flag.DurationVar(&cfg.OffhookDelay, "offhook-delay", 0, "delay after idle event subscription before reporting off-hook")
	flag.DurationVar(&cfg.DigitDelay, "digit-delay", time.Second, "delay before reporting a completed digit map")
	flag.DurationVar(&cfg.CallHold, "call-hold", 12*time.Second, "maximum time before forced on-hook after reporting digits")
	flag.DurationVar(&cfg.Timeout, "timeout", 30*time.Second, "overall test timeout")
	flag.Parse()

	if cfg.Mode != "offhook-only" && cfg.Mode != "outbound" && cfg.Mode != "inbound-observe" && cfg.Mode != "inbound-answer" {
		log.Fatalf("unsupported mode %q", cfg.Mode)
	}
	if cfg.Mode == "outbound" {
		if cfg.Number == "" || strings.Trim(cfg.Number, "0123456789*#") != "" {
			log.Fatal("outbound mode requires a number containing only 0-9, *, and #")
		}
	}
	if _, err := formatObservedEvent("al/of", cfg.EventStyle); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	probe, err := newCallProbe(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer probe.conn.Close()
	defer probe.closeMedia()

	if err := probe.run(ctx); err != nil {
		log.Fatal(err)
	}
}

func newCallProbe(ctx context.Context, cfg options) (*callProbe, error) {
	remote, err := net.ResolveUDPAddr("udp", cfg.MGC)
	if err != nil {
		return nil, fmt.Errorf("resolve MGC: %w", err)
	}
	conn, err := socketbind.ListenUDP(ctx, cfg.Source, cfg.BindDevice)
	if err != nil {
		return nil, fmt.Errorf("listen H.248: %w", err)
	}
	seed := uint64(time.Now().UnixNano() & 0x7fffffff)
	return &callProbe{
		options:    cfg,
		conn:       conn,
		remote:     remote,
		nextTID:    seed,
		replyCache: make(map[string]string),
	}, nil
}

func (p *callProbe) run(ctx context.Context) error {
	fmt.Printf("H.248 call probe %s -> %s, mId=%s, termination=%s, mode=%s\n",
		p.conn.LocalAddr(), p.remote, p.MID, p.Termination, p.Mode)
	if err := p.register(ctx); err != nil {
		return err
	}
	if p.LineRestart {
		p.deferLineEvents = true
		if err := p.registerTermination(ctx, p.Termination); err != nil {
			return err
		}
		p.deferLineEvents = false
		if p.latestRequestID != "" && !p.initialOnhook {
			if err := p.sendNotify("al/on", p.latestRequestID, "initial-onhook"); err != nil {
				return err
			}
		}
	}
	defer func() {
		if p.offhookSent && !p.onhookSent && p.latestRequestID != "" {
			_ = p.sendNotify("al/on", p.latestRequestID, "final-onhook")
		}
	}()

	deadline := time.Now().Add(p.Timeout)
	for time.Now().Before(deadline) {
		if !p.offhookAt.IsZero() && !p.offhookSent && !time.Now().Before(p.offhookAt) {
			if err := p.sendNotify("al/of", p.latestRequestID, "offhook"); err != nil {
				return err
			}
			p.offhookAt = time.Time{}
		}
		if !p.digitsAt.IsZero() && !p.digitsSent && !time.Now().Before(p.digitsAt) {
			if err := p.sendDigitMapComplete(p.latestRequestID); err != nil {
				return err
			}
			p.onhookAt = time.Now().Add(p.CallHold)
			fmt.Printf("digits reported; forced on-hook scheduled after %s\n", p.CallHold)
		}
		if !p.onhookAt.IsZero() && !p.onhookSent && !time.Now().Before(p.onhookAt) {
			if err := p.sendNotify("al/on", p.latestRequestID, "final-onhook"); err != nil {
				return err
			}
		}
		if !p.finishAt.IsZero() && !time.Now().Before(p.finishAt) {
			return nil
		}

		readUntil := time.Now().Add(500 * time.Millisecond)
		if readUntil.After(deadline) {
			readUntil = deadline
		}
		_ = p.conn.SetReadDeadline(readUntil)
		data, remote, err := readDatagram(p.conn)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if !sameUDPAddr(remote, p.remote) {
			fmt.Printf("ignoring H.248 datagram from non-active MGC %s\n", remote)
			continue
		}
		if err := p.handleDatagram(data); err != nil {
			return err
		}
	}

	return fmt.Errorf("test timed out after %s", p.Timeout)
}

func (p *callProbe) register(ctx context.Context) error {
	p.serviceChangeTID = p.newTID()
	request := h248.BuildServiceChange(h248.ServiceChangeOptions{
		Version:       1,
		MID:           p.MID,
		TransactionID: p.serviceChangeTID,
		Method:        "Restart",
		Reason:        "901 Cold Boot",
	})

	for attempt := 1; attempt <= 3; attempt++ {
		if err := p.send(request); err != nil {
			return err
		}
		fmt.Printf("TX ServiceChange attempt %d/3 transaction=%s\n%s", attempt, p.serviceChangeTID, request)
		end := time.Now().Add(3 * time.Second)
		for time.Now().Before(end) {
			_ = p.conn.SetReadDeadline(end)
			data, remote, err := readDatagram(p.conn)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					break
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
			if !sameUDPAddr(remote, p.remote) {
				continue
			}
			fmt.Printf("RX %s\n%s\n", remote, data)
			msg, err := h248.Parse([]byte(data))
			if err != nil {
				return fmt.Errorf("parse ServiceChange response: %w", err)
			}
			if msg.TransactionType == "Reply" && msg.TransactionID == p.serviceChangeTID {
				if msg.ErrorCode != "" {
					return fmt.Errorf("ServiceChange rejected with H.248 error %s", msg.ErrorCode)
				}
				fmt.Printf("ServiceChange active: version=%d MGC=%s\n", msg.Version, msg.MID)
				return nil
			}
		}
	}
	return fmt.Errorf("no ServiceChange reply from %s", p.remote)
}

func (p *callProbe) registerTermination(ctx context.Context, termination string) error {
	tid := p.newTID()
	request := h248.BuildServiceChange(h248.ServiceChangeOptions{
		Version:       1,
		MID:           p.MID,
		TransactionID: tid,
		Termination:   termination,
		Method:        "Restart",
		Reason:        "901 Cold Boot",
	})
	if err := p.send(request); err != nil {
		return err
	}
	fmt.Printf("TX physical-termination ServiceChange transaction=%s termination=%s\n%s", tid, termination, request)

	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		_ = p.conn.SetReadDeadline(end)
		data, remote, err := readDatagram(p.conn)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if !sameUDPAddr(remote, p.remote) {
			continue
		}
		fmt.Printf("RX %s\n%s\n", remote, data)
		msg, err := h248.Parse([]byte(data))
		if err != nil {
			return fmt.Errorf("parse physical-termination ServiceChange response: %w", err)
		}
		if msg.TransactionType == "Reply" && msg.TransactionID == tid {
			if msg.ErrorCode != "" {
				return fmt.Errorf("physical-termination ServiceChange rejected with H.248 error %s", msg.ErrorCode)
			}
			fmt.Printf("physical termination %s active\n", termination)
			return nil
		}
		if msg.TransactionType == "Transaction" {
			if err := p.handleMGCTransaction(msg); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("no ServiceChange reply for physical termination %s", termination)
}

func (p *callProbe) handleDatagram(data string) error {
	fmt.Printf("RX %s\n%s\n", p.remote, data)
	msg, err := h248.Parse([]byte(data))
	if err != nil {
		return fmt.Errorf("parse MGC message: %w", err)
	}

	switch msg.TransactionType {
	case "Reply":
		if msg.ErrorCode != "" {
			return fmt.Errorf("MGC returned H.248 error %s for transaction %s", msg.ErrorCode, msg.TransactionID)
		}
		switch msg.TransactionID {
		case p.initialOnhookTID:
			fmt.Printf("initial on-hook Notify accepted, transaction=%s\n", msg.TransactionID)
		case p.offhookTID:
			fmt.Printf("off-hook Notify accepted, transaction=%s\n", msg.TransactionID)
		case p.digitsTID:
			fmt.Printf("digit-map completion Notify accepted, transaction=%s number=%s\n", msg.TransactionID, p.Number)
		case p.onhookTID:
			fmt.Printf("on-hook Notify accepted, transaction=%s\n", msg.TransactionID)
			p.onhookTID = ""
			p.finishAt = time.Now().Add(3 * time.Second)
		}
		return nil
	case "Pending":
		fmt.Printf("transaction pending: %s\n", msg.TransactionID)
		return nil
	case "Transaction":
		return p.handleMGCTransaction(msg)
	default:
		return fmt.Errorf("unsupported transaction type %q", msg.TransactionType)
	}
}

func (p *callProbe) handleMGCTransaction(msg *h248.Message) error {
	if cached := p.replyCache[msg.TransactionID]; cached != "" {
		fmt.Printf("TX cached reply transaction=%s\n%s", msg.TransactionID, cached)
		return p.send(cached)
	}
	if (p.Mode == "inbound-answer" || p.Mode == "outbound") && isCallSetupAdd(msg, p.Termination) {
		return p.handleIncomingAdd(msg)
	}

	reply, err := buildCommandReply(msg, p.MID)
	if err != nil {
		return err
	}
	p.replyCache[msg.TransactionID] = reply
	if err := p.send(reply); err != nil {
		return err
	}
	fmt.Printf("TX reply transaction=%s\n%s", msg.TransactionID, reply)

	if requestID := extractEventRequestID(msg.Raw); requestID != "" {
		p.latestRequestID = requestID
		fmt.Printf("event subscription request-id=%s\n", requestID)
		if p.deferLineEvents {
			fmt.Println("deferring line event until physical-termination ServiceChange completes")
			return nil
		}
		if !p.initialOnhook {
			return p.sendNotify("al/on", requestID, "initial-onhook")
		}
		if !p.offhookSent && strings.EqualFold(msg.Commands[0].TerminationID, p.Termination) {
			if p.Mode == "inbound-observe" || p.Mode == "inbound-answer" {
				if containsRingSignal(msg.Raw) {
					fmt.Printf("INBOUND RING COMMAND DETECTED on %s\n", p.Termination)
					if p.Mode == "inbound-answer" {
						return p.sendNotify("al/of", requestID, "offhook")
					}
				} else {
					fmt.Printf("inbound observer ready on %s; waiting for carrier ring command\n", p.Termination)
				}
				return nil
			}
			if p.OffhookDelay > 0 {
				if p.offhookAt.IsZero() {
					p.offhookAt = time.Now().Add(p.OffhookDelay)
					fmt.Printf("idle subscription active; off-hook scheduled after %s\n", p.OffhookDelay)
				}
				return nil
			}
			return p.sendNotify("al/of", requestID, "offhook")
		}
		if p.Mode == "outbound" && !p.digitsSent && p.digitsAt.IsZero() {
			p.digitsAt = time.Now().Add(p.DigitDelay)
			fmt.Printf("post-offhook Modify accepted; will report number after %s\n", p.DigitDelay)
		} else if p.Mode == "offhook-only" && p.onhookAt.IsZero() {
			p.onhookAt = time.Now().Add(p.Hold)
			fmt.Printf("post-offhook Modify accepted; will send al/on after %s\n", p.Hold)
		}
	}
	return nil
}

func (p *callProbe) sendDigitMapComplete(requestID string) error {
	if requestID == "" {
		return fmt.Errorf("cannot report digits without an Events request ID")
	}
	tid := p.newTID()
	message := buildDigitMapCompleteNotify(
		p.MID,
		tid,
		p.Termination,
		requestID,
		formatH248Time(time.Now()),
		p.Number,
	)
	if err := p.send(message); err != nil {
		return err
	}
	p.digitsSent = true
	p.digitsTID = tid
	fmt.Printf("TX digit-map completion request-id=%s transaction=%s number=%s\n%s", requestID, tid, p.Number, message)
	return nil
}

func (p *callProbe) sendNotify(event, requestID, phase string) error {
	if requestID == "" {
		return fmt.Errorf("cannot send %s without an Events request ID", event)
	}
	tid := p.newTID()
	observedEvent, err := formatObservedEvent(event, p.EventStyle)
	if err != nil {
		return err
	}
	contextID := "-"
	if p.incomingSetup && p.contextID != "" {
		contextID = p.contextID
	}
	message := buildNotify(p.MID, tid, contextID, p.Termination, requestID, formatH248Time(time.Now()), observedEvent)
	if err := p.send(message); err != nil {
		return err
	}
	fmt.Printf("TX Notify event=%s request-id=%s transaction=%s\n%s", event, requestID, tid, message)
	switch phase {
	case "initial-onhook":
		p.initialOnhook = true
		p.initialOnhookTID = tid
	case "offhook":
		p.offhookSent = true
		p.offhookTID = tid
		if p.Mode == "inbound-answer" {
			p.onhookAt = time.Now().Add(p.CallHold)
			fmt.Printf("inbound call answered; forced on-hook scheduled after %s\n", p.CallHold)
		}
	case "final-onhook":
		p.onhookSent = true
		p.onhookTID = tid
	default:
		return fmt.Errorf("unknown Notify phase %q", phase)
	}
	return nil
}

func (p *callProbe) handleIncomingAdd(msg *h248.Message) error {
	if err := p.ensureMediaSocket(); err != nil {
		return err
	}
	if p.contextID == "" {
		p.contextID = strconv.FormatInt(time.Now().UnixNano()&0x7fffffff, 10)
	}
	if p.ephemeralTerm == "" {
		p.ephemeralTerm = "RTP/1"
	}
	reply := buildIncomingAddReply(
		p.MID,
		msg.TransactionID,
		p.contextID,
		p.Termination,
		p.ephemeralTerm,
		p.RTPIP,
		p.RTPPort,
	)
	p.replyCache[msg.TransactionID] = reply
	if err := p.send(reply); err != nil {
		return err
	}
	p.incomingSetup = true
	if requestID := extractEventRequestID(msg.Raw); requestID != "" {
		p.latestRequestID = requestID
	}
	fmt.Printf("INBOUND ADD accepted locally: context=%s physical=%s ephemeral=%s RTP=%s:%d\n%s",
		p.contextID, p.Termination, p.ephemeralTerm, p.RTPIP, p.RTPPort, reply)
	return nil
}

func (p *callProbe) ensureMediaSocket() error {
	if p.mediaConn != nil {
		return nil
	}
	address := net.JoinHostPort(p.RTPIP, strconv.Itoa(p.RTPPort))
	conn, err := socketbind.ListenUDP(context.Background(), address, p.BindDevice)
	if err != nil {
		return fmt.Errorf("listen RTP %s: %w", address, err)
	}
	p.mediaConn = conn
	return nil
}

func (p *callProbe) closeMedia() {
	if p.mediaConn != nil {
		_ = p.mediaConn.Close()
	}
}

func (p *callProbe) send(message string) error {
	_, err := p.conn.WriteToUDP([]byte(message), p.remote)
	return err
}

func (p *callProbe) newTID() string {
	p.nextTID++
	return strconv.FormatUint(p.nextTID, 10)
}

func buildCommandReply(msg *h248.Message, mid string) (string, error) {
	if len(msg.Commands) == 0 {
		return "", fmt.Errorf("transaction %s contains no supported command", msg.TransactionID)
	}
	actions := make([]h248.ActionReply, 0, 2)
	indices := make(map[string]int)
	for _, command := range msg.Commands {
		if h248.CompactCommand(command.Name) == "" {
			return "", fmt.Errorf("unsupported MGC command %q", command.Name)
		}
		termination := command.TerminationID
		if termination == "" {
			return "", fmt.Errorf("MGC command %s has no termination", command.Name)
		}
		contextID := command.ContextID
		if contextID == "" {
			contextID = msg.ContextID
		}
		if contextID == "" {
			contextID = "-"
		}
		index, exists := indices[contextID]
		if !exists {
			indices[contextID] = len(actions)
			actions = append(actions, h248.ActionReply{ContextID: contextID})
			index = len(actions) - 1
		}
		actions[index].Commands = append(actions[index].Commands, h248.SimpleCommandReply(command))
	}
	return h248.BuildActionReply(msg, mid, actions), nil
}

func buildNotify(mid, transactionID, contextID, termination, requestID, observedAt, event string) string {
	if contextID == "" {
		contextID = "-"
	}
	return fmt.Sprintf("!/1 %s\nT=%s{C=%s{N=%s{OE=%s{%s:%s}}}}\n",
		h248.FormatMID(mid), transactionID, contextID, termination, requestID, observedAt, event)
}

func buildDigitMapCompleteNotify(mid, transactionID, termination, requestID, observedAt, number string) string {
	return fmt.Sprintf("!/1 %s\nT=%s{C=-{N=%s{OE=%s{%s:dd/ce{ds=\"%s\",Meth=UM}}}}}\n",
		h248.FormatMID(mid), transactionID, termination, requestID, observedAt, number)
}

func buildIncomingAddReply(mid, transactionID, contextID, physicalTermination, ephemeralTermination, rtpIP string, rtpPort int) string {
	return fmt.Sprintf("!/1 %s\nP=%s{C=%s{A=%s,A=%s{M{L{v=0\r\nc=IN IP4 %s\r\nm=audio %d RTP/AVP 8 102\r\na=ptime:20\r\na=rtpmap:102 telephone-event/8000\r\na=fmtp:102 0-15\r\n}}}}}\n",
		h248.FormatMID(mid), transactionID, contextID, physicalTermination, ephemeralTermination, rtpIP, rtpPort)
}

func formatH248Time(value time.Time) string {
	return value.Format("20060102T150405") + fmt.Sprintf("%02d", value.Nanosecond()/10_000_000)
}

func formatObservedEvent(event, style string) (string, error) {
	switch style {
	case "plain":
		return event, nil
	case "paren-init":
		return event + "(init=false)", nil
	case "brace-init":
		return event + "{init=false}", nil
	default:
		return "", fmt.Errorf("unsupported event style %q", style)
	}
}

func extractEventRequestID(raw string) string {
	match := eventRequestRe.FindStringSubmatch(raw)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func containsRingSignal(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.Contains(lower, "al/ri") || strings.Contains(lower, "cg/rt") || strings.Contains(lower, "andisp/dwa")
}

func isCallSetupAdd(msg *h248.Message, physicalTermination string) bool {
	if msg == nil || msg.ContextID != "$" {
		return false
	}
	foundPhysical := false
	foundEphemeral := false
	for _, command := range msg.Commands {
		if command.Name != "Add" {
			continue
		}
		if strings.EqualFold(command.TerminationID, physicalTermination) {
			foundPhysical = true
		}
		if command.TerminationID == "$" {
			foundEphemeral = true
		}
	}
	return foundPhysical && foundEphemeral
}

func readDatagram(conn *net.UDPConn) (string, *net.UDPAddr, error) {
	buf := make([]byte, 65535)
	n, remote, err := conn.ReadFromUDP(buf)
	if err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(string(buf[:n])), remote, nil
}

func sameUDPAddr(left, right *net.UDPAddr) bool {
	return left != nil && right != nil && left.Port == right.Port && left.IP.Equal(right.IP)
}
