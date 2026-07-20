package bridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yunisky/megaco-sip-gateway/internal/config"
	"github.com/Yunisky/megaco-sip-gateway/internal/h248"
	"github.com/Yunisky/megaco-sip-gateway/internal/sip"
)

type fakeH248Responder struct {
	mu        sync.Mutex
	replies   []string
	generated []string
	local     *net.UDPAddr
	remote    *net.UDPAddr
}

func (f *fakeH248Responder) Respond(message string) error {
	f.mu.Lock()
	f.replies = append(f.replies, message)
	f.mu.Unlock()
	return nil
}

func (f *fakeH248Responder) SendTo(message string, _ *net.UDPAddr) error {
	f.mu.Lock()
	f.generated = append(f.generated, message)
	f.mu.Unlock()
	return nil
}

func (f *fakeH248Responder) LocalAddr() net.Addr      { return f.local }
func (f *fakeH248Responder) RemoteAddr() *net.UDPAddr { return cloneUDPAddr(f.remote) }

func (f *fakeH248Responder) lastReply() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.replies) == 0 {
		return ""
	}
	return f.replies[len(f.replies)-1]
}

func (f *fakeH248Responder) generatedMessages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.generated...)
}

type fakeSIPTransport struct {
	mu       sync.Mutex
	messages []string
	remote   []*net.UDPAddr
	local    *net.UDPAddr
}

func (f *fakeSIPTransport) Respond(message string) error {
	return f.SendTo(message, nil)
}

func (f *fakeSIPTransport) SendTo(message string, remote *net.UDPAddr) error {
	f.mu.Lock()
	f.messages = append(f.messages, message)
	f.remote = append(f.remote, cloneUDPAddr(remote))
	f.mu.Unlock()
	return nil
}

func (f *fakeSIPTransport) LocalAddr() net.Addr { return f.local }

func (f *fakeSIPTransport) firstMessageWith(prefix string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, message := range f.messages {
		if strings.HasPrefix(message, prefix) {
			return message
		}
	}
	return ""
}

func (f *fakeSIPTransport) messagesWith(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	messages := make([]string, 0)
	for _, message := range f.messages {
		if strings.HasPrefix(message, prefix) {
			messages = append(messages, message)
		}
	}
	return messages
}

func TestCarrierInboundSIPAnswerAndBidirectionalRTP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	carrierRTP, carrierRTCP := listenUDP4Pair(t)
	defer carrierRTP.Close()
	defer carrierRTCP.Close()
	pbxRTP, pbxRTCP := listenUDP4Pair(t)
	defer pbxRTP.Close()
	defer pbxRTCP.Close()
	portMin := findFreeEvenPortPair(t)

	cfg := config.Default()
	cfg.SIP.Listen = "127.0.0.1:5060"
	cfg.SIP.AdvertisedAddress = "127.0.0.1:5060"
	cfg.SIP.Domain = "test.local"
	cfg.SIP.TrunkURI = "sip:6000@127.0.0.1:5060"
	cfg.SIP.OutboundProxy = "127.0.0.1:5060"
	cfg.H248.MGID = "[198.51.100.10]:2944"
	cfg.H248.PhysicalTermination = "A0"
	cfg.H248.EphemeralTerminationPrefix = "RTP/"
	cfg.Media.H248RTPIP = "127.0.0.1"
	cfg.Media.RTPIP = "127.0.0.1"
	cfg.Media.PortMin = portMin
	cfg.Media.PortMax = portMin + 20
	cfg.Media.H248DTMFPayloadType = 102
	cfg.Media.DTMFPayloadType = 101

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	gateway := New(logger, cfg)
	defer gateway.Close()
	sipTransport := &fakeSIPTransport{local: udpAddr("127.0.0.1", 5060)}
	gateway.SetSIPSender(sipTransport)
	h248Transport := &fakeH248Responder{
		local:  udpAddr("198.51.100.10", 2944),
		remote: udpAddr("198.51.100.20", 2944),
	}

	gateway.HandleH248Ready(ctx, h248Transport)
	lineModify := mustParseH248(t, `!/1 [198.51.100.20]:2944 T=1{C=-{MF=A0{E=3002{al/*},SG{}}}}`)
	gateway.HandleH248(ctx, lineModify, h248Transport)
	if !containsMessage(h248Transport.generatedMessages(), "al/on") {
		t.Fatal("initial al/on was not generated")
	}

	carrierPort := carrierRTP.LocalAddr().(*net.UDPAddr).Port
	addRaw := fmt.Sprintf("!/1 [198.51.100.20]:2944 T=2{C=${A=A0{M{O{MO=SR}},E=3003{al/*},SG{}},A=${M{O{MO=IN},L{v=0\r\nc=IN IP4 $\r\nm=audio $ RTP/AVP 8 102\r\n},R{v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio %d RTP/AVP 8 102\r\na=ptime:20\r\na=rtpmap:102 telephone-event/8000\r\na=fmtp:102 0-15\r\n}}}}}", carrierPort)
	gateway.HandleH248(ctx, mustParseH248(t, addRaw), h248Transport)
	addReply := h248Transport.lastReply()
	if !strings.Contains(addReply, "A=A0,A=RTP/1{") || !strings.Contains(addReply, fmt.Sprintf("m=audio %d RTP/AVP 8 102", portMin)) {
		t.Fatalf("unexpected Add reply:\n%s", addReply)
	}

	ring := mustParseH248(t, `!/1 [198.51.100.20]:2944 T=3{C=100{MF=A0{E=3004{al/*},SG{andisp/dwa{ddb=04133031313230303231313535353031323334353624,pattern=1}}}}}`)
	// Use the context allocated by the gateway, not the fixture placeholder.
	gateway.mu.Lock()
	var call *Call
	for _, candidate := range gateway.callsByH248 {
		call = candidate
		break
	}
	gateway.mu.Unlock()
	if call == nil {
		t.Fatal("H.248 call was not allocated")
	}
	ring.ContextID = call.H248ContextID
	for index := range ring.Commands {
		ring.Commands[index].ContextID = call.H248ContextID
	}
	gateway.HandleH248(ctx, ring, h248Transport)

	inviteRaw := sipTransport.firstMessageWith("INVITE ")
	if inviteRaw == "" {
		t.Fatal("SIP INVITE was not generated")
	}
	if !strings.Contains(inviteRaw, "P-Asserted-Identity: <sip:15550123456@test.local>") {
		t.Fatalf("caller ID was not mapped to SIP:\n%s", inviteRaw)
	}
	invite, err := sip.Parse([]byte(inviteRaw))
	if err != nil {
		t.Fatal(err)
	}
	localOffer, err := sip.ParseSDP(invite.Body)
	if err != nil {
		t.Fatal(err)
	}
	if localOffer.Port != portMin+2 || localOffer.TelephoneEventPayload != 101 {
		t.Fatalf("SIP offer = %#v", localOffer)
	}

	pbxPort := pbxRTP.LocalAddr().(*net.UDPAddr).Port
	answerSDP := sip.BuildSDP("pbx", "127.0.0.1", pbxPort, []string{"PCMA/8000"}, 101)
	responseRaw := sip.BuildResponse(invite, 200, "OK", answerSDP, "sip:pbx@127.0.0.1:5060", nil)
	response, err := sip.Parse([]byte(responseRaw))
	if err != nil {
		t.Fatal(err)
	}
	response.Source = udpAddr("127.0.0.1", 5060)
	sipResponses := &fakeSIPTransport{local: udpAddr("127.0.0.1", 5060)}
	gateway.HandleSIP(ctx, response, sipResponses)
	if sipResponses.firstMessageWith("ACK ") == "" {
		t.Fatal("SIP ACK was not generated")
	}
	if !containsMessage(h248Transport.generatedMessages(), "al/of") {
		t.Fatal("H.248 off-hook was not generated after SIP 200 OK")
	}

	carrierDestination := udpAddr("127.0.0.1", call.H248RTPPort)
	pbxDestination := udpAddr("127.0.0.1", call.SIPRTPPort)
	assertRTPForward(t, carrierRTP, carrierDestination, pbxRTP, 102, 101)
	assertRTPForward(t, pbxRTP, pbxDestination, carrierRTP, 101, 102)
	assertUDPForward(t, carrierRTCP, udpAddr("127.0.0.1", call.H248RTPPort+1), pbxRTCP, []byte{0x80, 201, 0, 1, 0, 0, 0, 1})
	assertUDPForward(t, pbxRTCP, udpAddr("127.0.0.1", call.SIPRTPPort+1), carrierRTCP, []byte{0x80, 200, 0, 1, 0, 0, 0, 2})
}

func TestSIPOriginatedCallDigitMapContextMediaAndRelease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pbxRTP := listenUDP4(t)
	defer pbxRTP.Close()
	carrierRTP := listenUDP4(t)
	defer carrierRTP.Close()
	portMin := findFreeEvenPortPair(t)

	cfg := config.Default()
	cfg.SIP.Listen = "127.0.0.1:5060"
	cfg.SIP.AdvertisedAddress = "127.0.0.1:5060"
	cfg.H248.MGID = "[198.51.100.10]:2944"
	cfg.H248.PhysicalTermination = "A0"
	cfg.H248.OriginationStabilizationSeconds = 0
	cfg.H248.DigitReportDelayMilliseconds = 0
	cfg.Media.H248RTPIP = "127.0.0.1"
	cfg.Media.RTPIP = "127.0.0.1"
	cfg.Media.PortMin = portMin
	cfg.Media.PortMax = portMin + 20
	cfg.Media.H248DTMFPayloadType = 102
	cfg.Media.DTMFPayloadType = 97

	gateway := New(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
	defer gateway.Close()
	sipTransport := &fakeSIPTransport{local: udpAddr("127.0.0.1", 5060)}
	gateway.SetSIPSender(sipTransport)
	h248Transport := &fakeH248Responder{local: udpAddr("198.51.100.10", 2944), remote: udpAddr("198.51.100.20", 2944)}
	gateway.HandleH248Ready(ctx, h248Transport)
	gateway.HandleH248(ctx, mustParseH248(t, `!/1 [198.51.100.20]:2944 T=11{C=-{MF=A0{E=3002{al/*},SG{}}}}`), h248Transport)

	pbxPort := pbxRTP.LocalAddr().(*net.UDPAddr).Port
	offer := sip.BuildSDP("pbx", "127.0.0.1", pbxPort, []string{"PCMA/8000"}, 102)
	inviteRaw := sip.BuildRequest("INVITE", "sip:15550123456@gateway.test", map[string]string{
		"Via":     "SIP/2.0/UDP 127.0.0.1:5070;branch=z9hG4bK-outbound;rport",
		"From":    "<sip:6000@pbx.test>;tag=pbx-tag",
		"To":      "<sip:15550123456@gateway.test>",
		"Call-ID": "sip-outbound-test",
		"CSeq":    "1 INVITE",
		"Contact": "<sip:6000@127.0.0.1:5070>",
	}, offer)
	invite, err := sip.Parse([]byte(inviteRaw))
	if err != nil {
		t.Fatal(err)
	}
	invite.Source = udpAddr("127.0.0.1", 5070)
	gateway.HandleSIP(ctx, invite, sipTransport)
	if sipTransport.firstMessageWith("SIP/2.0 100 Trying") == "" {
		t.Fatal("SIP 100 Trying was not generated")
	}
	waitFor(t, func() bool { return containsMessage(h248Transport.generatedMessages(), "al/of") }, "H.248 origination off-hook")

	digitMap := `!/1 [198.51.100.20]:2944 T=12{C=-{MF=A0{E=3004{dd/ce{DM=dmap1},al/*},SG{cg/dt},DM=dmap1{(1555xxxxxxx)}}}}`
	gateway.HandleH248(ctx, mustParseH248(t, digitMap), h248Transport)
	waitFor(t, func() bool {
		return containsMessage(h248Transport.generatedMessages(), `dd/ce{ds="15550123456",Meth=UM}`)
	}, "H.248 digit-map completion")

	gateway.mu.Lock()
	call := gateway.outboundCall
	gateway.mu.Unlock()
	if call == nil {
		t.Fatal("outbound call was not allocated")
	}
	add := `!/1 [198.51.100.20]:2944 T=13{C=${A=A0{M{O{MO=IN}},E=3003{al/*},SG{}},A=${M{O{MO=RC},L{v=0\r\nc=IN IP4 $\r\nm=audio $ RTP/AVP 8 97\r\na=ptime:20\r\na=rtpmap:97 telephone-event/8000\r\n}}}}}`
	gateway.HandleH248(ctx, mustParseH248(t, add), h248Transport)
	if !strings.Contains(h248Transport.lastReply(), "C="+call.H248ContextID+"{A=A0,A="+call.H248EphemeralTerm) {
		t.Fatalf("outbound Add reply:\n%s", h248Transport.lastReply())
	}
	if !strings.Contains(h248Transport.lastReply(), "m=audio "+strconv.Itoa(call.H248RTPPort)+" RTP/AVP 8 97") {
		t.Fatalf("outbound Add reply did not accept MGC telephone-event PT97:\n%s", h248Transport.lastReply())
	}
	if sipTransport.firstMessageWith("SIP/2.0 183 Session Progress") == "" {
		t.Fatal("SIP 183 Session Progress was not generated")
	}

	carrierPort := carrierRTP.LocalAddr().(*net.UDPAddr).Port
	modify := fmt.Sprintf("!/1 [198.51.100.20]:2944 T=14{C=%s{MF=A0{M{O{MO=SR}},E=3001{ctyp/dtone,al/*},SG{}},MF=%s{M{O{MO=SR},R{v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio %d RTP/AVP 8 97\r\na=rtpmap:97 telephone-event/8000\r\n}}}}}", call.H248ContextID, call.H248EphemeralTerm, carrierPort)
	gateway.HandleH248(ctx, mustParseH248(t, modify), h248Transport)
	if sipTransport.firstMessageWith("SIP/2.0 200 OK") == "" {
		t.Fatal("SIP 200 OK was not generated after H.248 media activation")
	}
	assertRTPForward(t, pbxRTP, udpAddr("127.0.0.1", call.SIPRTPPort), carrierRTP, 102, 97)
	assertRTPForward(t, carrierRTP, udpAddr("127.0.0.1", call.H248RTPPort), pbxRTP, 97, 102)

	byeRaw := sip.BuildRequest("BYE", "sip:gateway@127.0.0.1:5060", map[string]string{
		"Via":     "SIP/2.0/UDP 127.0.0.1:5070;branch=z9hG4bK-bye;rport",
		"From":    "<sip:6000@pbx.test>;tag=pbx-tag",
		"To":      "<sip:15550123456@gateway.test>;tag=gw",
		"Call-ID": "sip-outbound-test",
		"CSeq":    "2 BYE",
	}, "")
	bye, err := sip.Parse([]byte(byeRaw))
	if err != nil {
		t.Fatal(err)
	}
	bye.Source = udpAddr("127.0.0.1", 5070)
	gateway.HandleSIP(ctx, bye, sipTransport)
	waitFor(t, func() bool {
		for _, message := range h248Transport.generatedMessages() {
			if strings.Contains(message, "C="+call.H248ContextID+"{N=A0") && strings.Contains(message, "al/on") {
				return true
			}
		}
		return false
	}, "H.248 context on-hook")

	subtract := fmt.Sprintf(`!/1 [198.51.100.20]:2944 T=15{C=%s{O-S=*}}`, call.H248ContextID)
	gateway.HandleH248(ctx, mustParseH248(t, subtract), h248Transport)
	if !strings.Contains(h248Transport.lastReply(), "C="+call.H248ContextID+"{S=*}") {
		t.Fatalf("Subtract reply:\n%s", h248Transport.lastReply())
	}
}

func TestModifyReplyGroupsBothCommandsInOneContext(t *testing.T) {
	cfg := config.Default()
	cfg.H248.MGID = "[198.51.100.10]:2944"
	gateway := New(slog.Default(), cfg)
	responder := &fakeH248Responder{local: udpAddr("127.0.0.1", 2944), remote: udpAddr("127.0.0.2", 2944)}
	request := mustParseH248(t, `!/1 [198.51.100.20]:2944 T=4002{C=2001{MF=A0{M{O{MO=SR}}},MF=RTP/1{M{O{MO=SR}}}}}`)
	gateway.HandleH248(context.Background(), request, responder)
	want := "C=2001{MF=A0,MF=RTP/1}"
	if !strings.Contains(responder.lastReply(), want) {
		t.Fatalf("reply does not contain %q:\n%s", want, responder.lastReply())
	}
	if strings.Count(responder.lastReply(), "C=2001{") != 1 {
		t.Fatalf("context was not grouped:\n%s", responder.lastReply())
	}
}

func TestCarrierSubtractAfterOffhookReportsOnhookInNullContext(t *testing.T) {
	cfg := config.Default()
	cfg.H248.MGID = "[198.51.100.10]:2944"
	gateway := New(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
	h248Transport := &fakeH248Responder{local: udpAddr("127.0.0.1", 2944), remote: udpAddr("127.0.0.2", 2944)}
	sipTransport := &fakeSIPTransport{local: udpAddr("127.0.0.1", 5060)}
	gateway.SetSIPSender(sipTransport)
	call := &Call{
		ID:                   "sip-carrier-subtract",
		Direction:            "sip-to-h248",
		State:                "sip-answered",
		H248ContextID:        "100",
		H248ContextAllocated: true,
		H248PhysicalTerm:     "A0",
		H248EventRequestID:   "77",
		H248Responder:        h248Transport,
		H248OffhookSent:      true,
		H248Answered:         true,
		SIPCallID:            "carrier-subtract-test",
		SIPPeer:              udpAddr("127.0.0.1", 5062),
		SIPFrom:              "<sip:6001@pbx.test>;tag=pbx",
		SIPTo:                "<sip:5550100@gateway.test>;tag=gw",
		SIPRemoteContact:     "sip:6001@127.0.0.1:5062",
		SIPAnswered:          true,
	}
	gateway.mu.Lock()
	gateway.callsByH248[call.H248ContextID] = call
	gateway.callsBySIP[call.SIPCallID] = call
	gateway.outboundCall = call
	gateway.mu.Unlock()

	subtract := mustParseH248(t, `!/1 [198.51.100.20]:2944 T=20{C=100{S=*}}`)
	gateway.HandleH248(context.Background(), subtract, h248Transport)

	if !containsMessage(h248Transport.generatedMessages(), "C=-{N=A0{OE=77{") ||
		!containsMessage(h248Transport.generatedMessages(), "al/on") {
		t.Fatalf("carrier Subtract did not produce null-Context on-hook:\n%v", h248Transport.generatedMessages())
	}
	if sipTransport.firstMessageWith("BYE ") == "" {
		t.Fatal("carrier Subtract did not tear down the answered SIP dialog")
	}
	if !call.H248OnhookSent || !call.Released {
		t.Fatalf("call state after carrier Subtract: onhook=%v released=%v", call.H248OnhookSent, call.Released)
	}
	gateway.mu.Lock()
	lineRequestID := gateway.line.eventRequestID
	_, byH248 := gateway.callsByH248[call.H248ContextID]
	_, bySIP := gateway.callsBySIP[call.SIPCallID]
	outbound := gateway.outboundCall
	gateway.mu.Unlock()
	if lineRequestID != "77" {
		t.Fatalf("null-Context line request ID = %q, want 77", lineRequestID)
	}
	if byH248 || bySIP || outbound != nil {
		t.Fatalf("call maps not cleared: byH248=%v bySIP=%v outbound=%v", byH248, bySIP, outbound)
	}
}

func TestH248UnavailableResetsLineAndReleasesSIPCall(t *testing.T) {
	cfg := config.Default()
	gateway := New(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
	sipTransport := &fakeSIPTransport{local: udpAddr("127.0.0.1", 5060)}
	gateway.SetSIPSender(sipTransport)
	call := &Call{
		ID:                   "h248-100",
		Direction:            "h248-to-sip",
		State:                "sip-ringing",
		H248ContextID:        "100",
		H248ContextAllocated: true,
		SIPCallID:            "mgc-failure-test",
		SIPRequestURI:        "sip:6000@127.0.0.1",
		SIPPeer:              udpAddr("127.0.0.1", 5070),
		SIPFrom:              "<sip:caller@test>;tag=gw",
		SIPTo:                "<sip:6000@test>",
		SIPInviteBranch:      "z9hG4bK-test",
		SIPInviteCSeq:        1,
	}
	gateway.mu.Lock()
	gateway.line.ready = true
	gateway.line.readyAt = time.Now()
	gateway.line.eventRequestID = "1"
	gateway.line.initialOnhookSent = true
	gateway.callsByH248[call.H248ContextID] = call
	gateway.callsBySIP[call.SIPCallID] = call
	gateway.mu.Unlock()

	gateway.HandleH248Unavailable(context.Background(), udpAddr("198.51.100.20", 2944))
	if !call.Released {
		t.Fatal("call was not released")
	}
	gateway.mu.Lock()
	lineReady := gateway.line.ready
	_, byH248 := gateway.callsByH248[call.H248ContextID]
	_, bySIP := gateway.callsBySIP[call.SIPCallID]
	gateway.mu.Unlock()
	if lineReady || byH248 || bySIP {
		t.Fatalf("state not cleared: lineReady=%v byH248=%v bySIP=%v", lineReady, byH248, bySIP)
	}
	if sipTransport.firstMessageWith("CANCEL ") == "" {
		t.Fatal("pending SIP INVITE was not cancelled")
	}
}

func TestCarrierCanceledInviteACKs487Retransmissions(t *testing.T) {
	cfg := config.Default()
	cfg.SIP.Listen = "127.0.0.1:5060"
	cfg.SIP.AdvertisedAddress = "127.0.0.1:5060"
	gateway := New(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
	defer gateway.Close()
	gateway.sipInviteTransactionTTL = 40 * time.Millisecond
	sipTransport := &fakeSIPTransport{local: udpAddr("127.0.0.1", 5060)}
	gateway.SetSIPSender(sipTransport)

	inviteVia := "SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bK-retained-487;rport"
	inviteRaw := sip.BuildRequest("INVITE", "sip:6000@127.0.0.1:5062", map[string]string{
		"Via":     inviteVia,
		"From":    "<sip:caller@gateway.test>;tag=local-tag",
		"To":      "<sip:6000@pbx.test>",
		"Call-ID": "retained-487-test",
		"CSeq":    "1 INVITE",
		"Contact": "<sip:gateway@127.0.0.1:5060>",
	}, "")
	call := &Call{
		ID:              "h248-retained-487",
		Direction:       "h248-to-sip",
		State:           "sip-ringing",
		H248ContextID:   "200",
		SIPCallID:       "retained-487-test",
		SIPRequestURI:   "sip:6000@127.0.0.1:5062",
		SIPPeer:         udpAddr("127.0.0.1", 5062),
		SIPFrom:         "<sip:caller@gateway.test>;tag=local-tag",
		SIPTo:           "<sip:6000@pbx.test>",
		SIPInvite:       inviteRaw,
		SIPInviteVia:    inviteVia,
		SIPInviteBranch: "z9hG4bK-retained-487",
		SIPInviteCSeq:   1,
	}
	gateway.mu.Lock()
	gateway.callsByH248[call.H248ContextID] = call
	gateway.callsBySIP[call.SIPCallID] = call
	gateway.mu.Unlock()

	gateway.handleCarrierRelease(call, false)
	if !call.Released {
		t.Fatal("carrier-released call remained active")
	}
	cancelMessages := sipTransport.messagesWith("CANCEL ")
	if len(cancelMessages) != 1 {
		t.Fatalf("CANCEL messages = %d, want 1", len(cancelMessages))
	}
	cancel, err := sip.Parse([]byte(cancelMessages[0]))
	if err != nil {
		t.Fatal(err)
	}
	if cancel.Header("Via") != inviteVia || cancel.Header("CSeq") != "1 CANCEL" {
		t.Fatalf("CANCEL did not reuse INVITE transaction:\n%s", cancelMessages[0])
	}
	gateway.mu.Lock()
	retained := gateway.retainedSIPInvites[call.SIPCallID]
	_, active := gateway.callsBySIP[call.SIPCallID]
	gateway.mu.Unlock()
	if retained == nil || active {
		t.Fatalf("retained transaction=%v active call mapping=%v", retained != nil, active)
	}

	cancelOK := mustParseSIP(t, sip.BuildResponse(cancel, 200, "OK", "", "", nil))
	cancelOK.Source = udpAddr("127.0.0.1", 5062)
	gateway.HandleSIP(context.Background(), cancelOK, sipTransport)
	if got := len(sipTransport.messagesWith("ACK ")); got != 0 {
		t.Fatalf("ACK count after CANCEL 200 = %d, want 0", got)
	}

	invite := mustParseSIP(t, inviteRaw)
	terminated := mustParseSIP(t, sip.BuildResponse(invite, 487, "Request Terminated", "", "", nil))
	terminated.Source = udpAddr("127.0.0.1", 5062)
	gateway.HandleSIP(context.Background(), terminated, sipTransport)
	gateway.HandleSIP(context.Background(), terminated, sipTransport)
	acks := sipTransport.messagesWith("ACK ")
	if len(acks) != 2 {
		t.Fatalf("ACK count for original and retransmitted 487 = %d, want 2", len(acks))
	}
	ack := mustParseSIP(t, acks[0])
	if ack.URI != invite.URI || ack.Header("Via") != inviteVia || ack.Header("CSeq") != "1 ACK" {
		t.Fatalf("non-2xx ACK did not preserve INVITE transaction:\n%s", acks[0])
	}
	if ack.Header("From") != invite.Header("From") || ack.Header("To") != terminated.Header("To") {
		t.Fatalf("non-2xx ACK dialog headers are incorrect:\n%s", acks[0])
	}

	waitFor(t, func() bool {
		gateway.mu.Lock()
		_, exists := gateway.retainedSIPInvites[call.SIPCallID]
		gateway.mu.Unlock()
		return !exists
	}, "retained INVITE Timer D expiry")
}

func TestFinalInviteFailurePreventsLateCarrierCancel(t *testing.T) {
	cfg := config.Default()
	cfg.SIP.Listen = "127.0.0.1:5060"
	cfg.SIP.AdvertisedAddress = "127.0.0.1:5060"
	gateway := New(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
	defer gateway.Close()
	sipTransport := &fakeSIPTransport{local: udpAddr("127.0.0.1", 5060)}
	gateway.SetSIPSender(sipTransport)

	inviteVia := "SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bK-final-404;rport"
	inviteRaw := sip.BuildRequest("INVITE", "sip:6000@127.0.0.1:5062", map[string]string{
		"Via":     inviteVia,
		"From":    "<sip:caller@gateway.test>;tag=local-tag",
		"To":      "<sip:6000@pbx.test>",
		"Call-ID": "final-404-test",
		"CSeq":    "1 INVITE",
		"Contact": "<sip:gateway@127.0.0.1:5060>",
	}, "")
	call := &Call{
		ID:              "h248-final-404",
		Direction:       "h248-to-sip",
		State:           "sip-invite-sent",
		H248ContextID:   "202",
		SIPCallID:       "final-404-test",
		SIPRequestURI:   "sip:6000@127.0.0.1:5062",
		SIPPeer:         udpAddr("127.0.0.1", 5062),
		SIPFrom:         "<sip:caller@gateway.test>;tag=local-tag",
		SIPTo:           "<sip:6000@pbx.test>",
		SIPInvite:       inviteRaw,
		SIPInviteVia:    inviteVia,
		SIPInviteBranch: "z9hG4bK-final-404",
		SIPInviteCSeq:   1,
	}
	gateway.mu.Lock()
	gateway.callsByH248[call.H248ContextID] = call
	gateway.callsBySIP[call.SIPCallID] = call
	gateway.mu.Unlock()

	invite := mustParseSIP(t, inviteRaw)
	notFound := mustParseSIP(t, sip.BuildResponse(invite, 404, "Not Found", "", "", nil))
	notFound.Source = udpAddr("127.0.0.1", 5062)
	gateway.HandleSIP(context.Background(), notFound, sipTransport)
	if !call.SIPInviteFinal {
		t.Fatal("final INVITE response was not recorded")
	}
	if got := len(sipTransport.messagesWith("ACK ")); got != 1 {
		t.Fatalf("ACK count after 404 = %d, want 1", got)
	}
	if got := len(sipTransport.messagesWith("CANCEL ")); got != 0 {
		t.Fatalf("CANCEL count before carrier release = %d, want 0", got)
	}

	gateway.handleCarrierRelease(call, false)
	if !call.Released {
		t.Fatal("call was not released after carrier clear")
	}
	if got := len(sipTransport.messagesWith("CANCEL ")); got != 0 {
		t.Fatalf("CANCEL count after final 404 and carrier release = %d, want 0", got)
	}

	// A retransmitted final response still belongs to Timer D and must receive
	// another transaction ACK even though the active Call has been released.
	gateway.HandleSIP(context.Background(), notFound, sipTransport)
	acks := sipTransport.messagesWith("ACK ")
	if len(acks) != 2 {
		t.Fatalf("ACK count after retransmitted 404 = %d, want 2", len(acks))
	}
	ack := mustParseSIP(t, acks[1])
	if ack.Header("Via") != inviteVia || ack.Header("CSeq") != "1 ACK" {
		t.Fatalf("retransmitted-404 ACK is incorrect:\n%s", acks[1])
	}
}

func TestCarrierCanceledInviteLate200IsACKedAndClearedOnce(t *testing.T) {
	cfg := config.Default()
	cfg.SIP.Listen = "127.0.0.1:5060"
	cfg.SIP.AdvertisedAddress = "127.0.0.1:5060"
	gateway := New(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
	defer gateway.Close()
	sipTransport := &fakeSIPTransport{local: udpAddr("127.0.0.1", 5060)}
	gateway.SetSIPSender(sipTransport)

	inviteVia := "SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bK-late-200;rport"
	inviteRaw := sip.BuildRequest("INVITE", "sip:6000@127.0.0.1:5062", map[string]string{
		"Via":     inviteVia,
		"From":    "<sip:caller@gateway.test>;tag=local-tag",
		"To":      "<sip:6000@pbx.test>",
		"Call-ID": "late-200-test",
		"CSeq":    "1 INVITE",
		"Contact": "<sip:gateway@127.0.0.1:5060>",
	}, "")
	call := &Call{
		ID:              "h248-late-200",
		Direction:       "h248-to-sip",
		State:           "sip-ringing",
		H248ContextID:   "201",
		SIPCallID:       "late-200-test",
		SIPRequestURI:   "sip:6000@127.0.0.1:5062",
		SIPPeer:         udpAddr("127.0.0.1", 5062),
		SIPFrom:         "<sip:caller@gateway.test>;tag=local-tag",
		SIPTo:           "<sip:6000@pbx.test>",
		SIPInvite:       inviteRaw,
		SIPInviteVia:    inviteVia,
		SIPInviteBranch: "z9hG4bK-late-200",
		SIPInviteCSeq:   1,
	}
	gateway.mu.Lock()
	gateway.callsByH248[call.H248ContextID] = call
	gateway.callsBySIP[call.SIPCallID] = call
	gateway.mu.Unlock()
	gateway.handleCarrierRelease(call, false)

	invite := mustParseSIP(t, inviteRaw)
	answered := mustParseSIP(t, sip.BuildResponse(invite, 200, "OK", "", "sip:pbx@127.0.0.1:5090", nil))
	answered.Source = udpAddr("127.0.0.1", 5062)
	gateway.HandleSIP(context.Background(), answered, sipTransport)
	gateway.HandleSIP(context.Background(), answered, sipTransport)

	acks := sipTransport.messagesWith("ACK ")
	byes := sipTransport.messagesWith("BYE ")
	if len(acks) != 2 {
		t.Fatalf("ACK count for original and retransmitted late 200 = %d, want 2", len(acks))
	}
	if len(byes) != 1 {
		t.Fatalf("BYE count for retransmitted late 200 = %d, want 1", len(byes))
	}
	ack := mustParseSIP(t, acks[0])
	if ack.URI != "sip:pbx@127.0.0.1:5090" || ack.Header("Via") == inviteVia || ack.Header("CSeq") != "1 ACK" {
		t.Fatalf("late-2xx ACK is incorrect:\n%s", acks[0])
	}
	bye := mustParseSIP(t, byes[0])
	if bye.URI != "sip:pbx@127.0.0.1:5090" || bye.Header("CSeq") != "2 BYE" || bye.Header("To") != answered.Header("To") {
		t.Fatalf("late-2xx BYE is incorrect:\n%s", byes[0])
	}

	byeOK := mustParseSIP(t, sip.BuildResponse(bye, 200, "OK", "", "", nil))
	byeOK.Source = udpAddr("127.0.0.1", 5062)
	gateway.HandleSIP(context.Background(), byeOK, sipTransport)
	if len(sipTransport.messagesWith("BYE ")) != 1 {
		t.Fatal("late-answer BYE response triggered a duplicate BYE")
	}
}

func TestRewriteTelephoneEventPayloadType(t *testing.T) {
	packet := makeRTPPacket(102)
	rewriteRTPPayloadType(packet, 102, 101)
	if got := int(packet[1] & 0x7f); got != 101 {
		t.Fatalf("payload type = %d", got)
	}
	if packet[1]&0x80 == 0 {
		t.Fatal("marker bit was not preserved")
	}
}

func TestSynthesizeTelephoneEventAsPCMA(t *testing.T) {
	packet := make([]byte, 16)
	packet[0] = 0x80
	packet[1] = 0x80 | 102
	binary.BigEndian.PutUint16(packet[2:4], 10)
	binary.BigEndian.PutUint32(packet[4:8], 8000)
	binary.BigEndian.PutUint32(packet[8:12], 0x12345678)
	packet[12] = 1
	packet[13] = 10
	binary.BigEndian.PutUint16(packet[14:16], 160)

	state := dtmfToneState{}
	first, ok := synthesizeTelephoneEventAsPCMA(packet, &state, 160)
	if !ok {
		t.Fatal("telephone-event packet was not synthesized")
	}
	if len(first) != 172 || int(first[1]&0x7f) != 8 || first[1]&0x80 == 0 {
		t.Fatalf("first synthesized RTP packet: length=%d payload=%d marker=%t", len(first), first[1]&0x7f, first[1]&0x80 != 0)
	}
	if timestamp := binary.BigEndian.Uint32(first[4:8]); timestamp != 8000 {
		t.Fatalf("first timestamp = %d", timestamp)
	}
	allSilence := true
	for _, sample := range first[12:] {
		if sample != 0xd5 {
			allSilence = false
			break
		}
	}
	if allSilence {
		t.Fatal("synthesized DTMF is silent")
	}

	packet[1] = 102
	binary.BigEndian.PutUint16(packet[2:4], 11)
	binary.BigEndian.PutUint16(packet[14:16], 320)
	second, ok := synthesizeTelephoneEventAsPCMA(packet, &state, 160)
	if !ok {
		t.Fatal("second telephone-event packet was not synthesized")
	}
	if timestamp := binary.BigEndian.Uint32(second[4:8]); timestamp != 8160 {
		t.Fatalf("second timestamp = %d", timestamp)
	}
}

func TestOutboundAddOmitsTelephoneEventInInbandMode(t *testing.T) {
	cfg := config.Default()
	cfg.H248.MGID = "[198.51.100.10]:2944"
	cfg.Media.H248DTMFMode = "inband"
	gateway := New(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
	defer gateway.Close()
	responder := &fakeH248Responder{local: udpAddr("198.51.100.10", 2944), remote: udpAddr("198.51.100.20", 2944)}
	call := &Call{
		ID:                   "sip-inband-dtmf",
		Direction:            "sip-to-h248",
		H248ContextID:        "100",
		H248PhysicalTerm:     "A0",
		H248EphemeralTerm:    "RTP/1",
		H248RTPPort:          4000,
		H248TelephoneEventPT: 102,
		SIPTelephoneEventPT:  102,
	}
	request := mustParseH248(t, "!/1 [198.51.100.20]:2944 T=13{C=${A=A0{M{O{MO=IN}}},A=${M{O{MO=RC},L{v=0\r\nc=IN IP4 $\r\nm=audio $ RTP/AVP 8 97\r\na=rtpmap:97 telephone-event/8000\r\n}}}}}")
	gateway.handleOutboundCallAdd(request, responder, call)
	reply := responder.lastReply()
	if !strings.Contains(reply, "m=audio 4000 RTP/AVP 8") || strings.Contains(reply, "telephone-event") {
		t.Fatalf("in-band Add reply advertised telephone-event:\n%s", reply)
	}
	if call.H248TelephoneEventPT != 0 {
		t.Fatalf("H.248 telephone-event payload = %d", call.H248TelephoneEventPT)
	}
}

func assertRTPForward(t *testing.T, source *net.UDPConn, destination *net.UDPAddr, receiver *net.UDPConn, sourcePT, expectedPT int) {
	t.Helper()
	packet := makeRTPPacket(sourcePT)
	if _, err := source.WriteToUDP(packet, destination); err != nil {
		t.Fatal(err)
	}
	if err := receiver.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 2048)
	n, _, err := receiver.ReadFromUDP(buffer)
	if err != nil {
		t.Fatalf("read relayed RTP: %v", err)
	}
	if n != len(packet) || int(buffer[1]&0x7f) != expectedPT {
		t.Fatalf("relayed RTP length=%d payload=%d, want length=%d payload=%d", n, int(buffer[1]&0x7f), len(packet), expectedPT)
	}
}

func assertUDPForward(t *testing.T, source *net.UDPConn, destination *net.UDPAddr, receiver *net.UDPConn, packet []byte) {
	t.Helper()
	if _, err := source.WriteToUDP(packet, destination); err != nil {
		t.Fatal(err)
	}
	if err := receiver.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 2048)
	count, _, err := receiver.ReadFromUDP(buffer)
	if err != nil {
		t.Fatalf("read relayed UDP: %v", err)
	}
	if count != len(packet) || string(buffer[:count]) != string(packet) {
		t.Fatalf("relayed UDP = %x, want %x", buffer[:count], packet)
	}
}

func makeRTPPacket(payloadType int) []byte {
	packet := make([]byte, 12+160)
	packet[0] = 0x80
	packet[1] = 0x80 | byte(payloadType)
	packet[2] = 0x00
	packet[3] = 0x01
	for index := 12; index < len(packet); index++ {
		packet[index] = 0xd5
	}
	return packet
}

func mustParseH248(t *testing.T, raw string) *h248.Message {
	t.Helper()
	message, err := h248.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func mustParseSIP(t *testing.T, raw string) *sip.Message {
	t.Helper()
	message, err := sip.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func containsMessage(messages []string, value string) bool {
	for _, message := range messages {
		if strings.Contains(message, value) {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func listenUDP4(t *testing.T) *net.UDPConn {
	t.Helper()
	connection, err := net.ListenUDP("udp4", udpAddr("127.0.0.1", 0))
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func listenUDP4Pair(t *testing.T) (*net.UDPConn, *net.UDPConn) {
	t.Helper()
	for port := 20000; port < 60000; port += 2 {
		rtp, err := net.ListenUDP("udp4", udpAddr("127.0.0.1", port))
		if err != nil {
			continue
		}
		rtcp, err := net.ListenUDP("udp4", udpAddr("127.0.0.1", port+1))
		if err == nil {
			return rtp, rtcp
		}
		_ = rtp.Close()
	}
	t.Fatal("no free RTP/RTCP port pair")
	return nil, nil
}

func findFreeEvenPortPair(t *testing.T) int {
	t.Helper()
	for port := 30000; port < 50000; port += 4 {
		connections := make([]*net.UDPConn, 0, 4)
		available := true
		for offset := 0; offset < 4; offset++ {
			connection, err := net.ListenUDP("udp4", udpAddr("127.0.0.1", port+offset))
			if err != nil {
				available = false
				break
			}
			connections = append(connections, connection)
		}
		for _, connection := range connections {
			_ = connection.Close()
		}
		if available {
			return port
		}
	}
	t.Fatal("no free RTP port pair")
	return 0
}

func udpAddr(ip string, port int) *net.UDPAddr {
	return &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
}

func TestResolveSIPPeerFromURI(t *testing.T) {
	peer, err := resolveSIPPeer("", "sip:6000@127.0.0.1:5070;transport=udp")
	if err != nil {
		t.Fatal(err)
	}
	if peer.Port != 5070 || !peer.IP.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("peer = %s", peer)
	}
	if got := advertisedSIPAddress("", "0.0.0.0:5060", "192.0.2.1"); got != "192.0.2.1:5060" {
		t.Fatalf("advertised address = %q", got)
	}
	if got := sanitizeSIPUser(" +1 (202) 555-0100 "); got != "+12025550100" {
		t.Fatalf("SIP user = %q", got)
	}
}
