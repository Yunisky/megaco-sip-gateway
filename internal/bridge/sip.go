package bridge

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Yunisky/megaco-sip-gateway/internal/sip"
)

func (b *Bridge) HandleSIP(ctx context.Context, msg *sip.Message, responder sip.Responder) {
	if msg.Status > 0 {
		b.handleSIPResponse(msg, responder)
		return
	}
	switch strings.ToUpper(msg.Method) {
	case "OPTIONS":
		_ = responder.Respond(sip.BuildResponse(msg, 200, "OK", "", localContact(b.cfg), map[string]string{
			"Allow":     "INVITE, ACK, CANCEL, BYE, OPTIONS, INFO, UPDATE",
			"Supported": "100rel, replaces, timer",
		}))
	case "ACK":
		b.logger.Debug("SIP ACK received", "call_id", msg.CallID())
	case "BYE":
		b.handleSIPBye(msg, responder)
	case "CANCEL":
		b.handleSIPCancel(msg, responder)
	case "INFO":
		_ = responder.Respond(sip.BuildResponse(msg, 200, "OK", "", "", nil))
	case "UPDATE":
		b.handleSIPSessionUpdate(msg, responder)
	case "INVITE":
		if headerParameter(msg.Header("To"), "tag") != "" && b.handleSIPReinvite(msg, responder) {
			return
		}
		b.handleNewSIPInvite(ctx, msg, responder)
	default:
		_ = responder.Respond(sip.BuildResponse(msg, 405, "Method Not Allowed", "", "", map[string]string{
			"Allow": "INVITE, ACK, CANCEL, BYE, OPTIONS, INFO, UPDATE",
		}))
	}
	_ = ctx
}

func (b *Bridge) handleNewSIPInvite(ctx context.Context, msg *sip.Message, responder sip.Responder) {
	if msg.CallID() == "" || msg.Source == nil {
		_ = responder.Respond(sip.BuildResponse(msg, 400, "Bad Request", "", "", nil))
		return
	}
	b.mu.Lock()
	if existing := b.callsBySIP[msg.CallID()]; existing != nil {
		response := existing.SIPLastResponse
		b.mu.Unlock()
		if response != "" {
			_ = responder.Respond(response)
		}
		return
	}
	b.mu.Unlock()

	destination := sipURIUser(msg.URI)
	if destination == "" || strings.Trim(destination, "0123456789*#") != "" {
		_ = responder.Respond(sip.BuildResponse(msg, 484, "Address Incomplete", "", localContact(b.cfg), nil))
		return
	}
	offer, err := sip.ParseSDP(msg.Body)
	if err != nil || !containsCodec(offer.Codecs, "PCMA/8000") {
		_ = responder.Respond(sip.BuildResponse(msg, 488, "Not Acceptable Here", "", localContact(b.cfg), nil))
		return
	}
	remoteMedia, err := net.ResolveUDPAddr("udp", net.JoinHostPort(offer.IP, strconv.Itoa(offer.Port)))
	if err != nil {
		_ = responder.Respond(sip.BuildResponse(msg, 488, "Not Acceptable Here", "", localContact(b.cfg), nil))
		return
	}

	b.mu.Lock()
	lineReady := b.line.ready && b.line.initialOnhookSent && b.line.eventRequestID != "" && b.line.responder != nil
	lineBusy := b.outboundCall != nil || len(b.callsByH248) > 0
	if !lineReady || lineBusy {
		b.mu.Unlock()
		status, reason := 503, "H248 Line Not Ready"
		if lineBusy {
			status, reason = 486, "Busy Here"
		}
		_ = responder.Respond(sip.BuildResponse(msg, status, reason, "", localContact(b.cfg), map[string]string{"Retry-After": "5"}))
		return
	}
	contextID, ephemeral, h248Port, sipPort := b.allocateCallIdentifiersLocked()
	sipDTMF := offer.TelephoneEventPayload
	if sipDTMF <= 0 {
		sipDTMF = b.cfg.Media.DTMFPayloadType
	}
	call := &Call{
		ID:                   "sip-" + msg.CallID(),
		Direction:            "sip-to-h248",
		State:                "sip-invite-received",
		CreatedAt:            time.Now(),
		H248ContextID:        contextID,
		H248PhysicalTerm:     b.cfg.H248.PhysicalTermination,
		H248EphemeralTerm:    ephemeral,
		H248EventRequestID:   b.line.eventRequestID,
		H248Responder:        b.line.responder,
		H248RTPPort:          h248Port,
		H248TelephoneEventPT: b.cfg.Media.H248DTMFPayloadType,
		SIPCallID:            msg.CallID(),
		SIPRequestURI:        msg.URI,
		SIPPeer:              cloneUDPAddr(msg.Source),
		SIPRemoteMedia:       cloneUDPAddr(remoteMedia),
		SIPRTPPort:           sipPort,
		SIPTelephoneEventPT:  sipDTMF,
		SIPFrom:              msg.Header("From"),
		SIPTo:                addHeaderParameter(msg.Header("To"), "tag", "gw"),
		SIPRemoteContact:     firstURI(msg.Header("Contact")),
		SIPInviteCSeq:        parseCSeqNumber(msg.CSeqNumber()),
		SIPInitialRequest:    msg,
		SIPResponder:         responder,
		DestinationNumber:    destination,
	}
	b.outboundCall = call
	b.callsBySIP[call.SIPCallID] = call
	b.mu.Unlock()

	relay, err := newMediaRelay(
		ctx,
		b.logger,
		call.ID,
		b.cfg.Media.H248RTPIP,
		call.H248RTPPort,
		b.cfg.H248.BindDevice,
		b.cfg.Media.RTPIP,
		call.SIPRTPPort,
		b.cfg.SIP.BindDevice,
		call.H248TelephoneEventPT,
		call.SIPTelephoneEventPT,
		b.cfg.Media.H248DTMFMode,
		b.cfg.Media.PacketizationTimeMillis,
	)
	if err != nil {
		b.failSIPOriginatedCall(call, 500, "RTP Resource Unavailable")
		b.releaseCall(call)
		return
	}
	relay.SetSIPPeer(remoteMedia, call.SIPTelephoneEventPT)
	b.mu.Lock()
	call.Media = relay
	b.mu.Unlock()

	b.sendSIPOriginatedResponse(call, 100, "Trying", false)
	b.scheduleOriginationOffhook(call)
	b.logger.Info("SIP-originated call queued for H.248",
		"call_id", call.SIPCallID,
		"destination", destination,
		"sip_rtp", remoteMedia,
		"local_sip_rtp_port", call.SIPRTPPort,
		"local_h248_rtp_port", call.H248RTPPort,
	)
}

func (b *Bridge) sendSIPOriginatedResponse(call *Call, status int, reason string, withSDP bool) bool {
	b.mu.Lock()
	if call == nil || call.Released || call.SIPInitialRequest == nil || call.SIPResponder == nil {
		b.mu.Unlock()
		return false
	}
	if call.SIPAnswered && status < 200 {
		b.mu.Unlock()
		return false
	}
	body := ""
	if withSDP {
		body = sip.BuildSDPWithPTime("h248gw", b.cfg.Media.RTPIP, call.SIPRTPPort, []string{"PCMA/8000"}, call.SIPTelephoneEventPT, b.cfg.Media.PacketizationTimeMillis)
	}
	response := sip.BuildResponse(call.SIPInitialRequest, status, reason, body, localContact(b.cfg), map[string]string{
		"Allow":     "INVITE, ACK, CANCEL, BYE, OPTIONS, INFO, UPDATE",
		"Supported": "100rel, replaces, timer",
	})
	responder := call.SIPResponder
	call.SIPLastResponse = response
	if status >= 200 && status < 300 {
		call.SIPAnswered = true
		call.State = "sip-answered"
	} else if status == 183 {
		call.State = "sip-early-media"
	}
	b.mu.Unlock()
	if err := responder.Respond(response); err != nil {
		b.logger.Warn("send SIP response", "call_id", call.SIPCallID, "status", status, "error", err)
		return false
	}
	return true
}

func (b *Bridge) answerSIPOriginatedCall(call *Call) {
	b.mu.Lock()
	answered := call == nil || call.Released || call.SIPAnswered
	b.mu.Unlock()
	if answered {
		return
	}
	if b.sendSIPOriginatedResponse(call, 200, "OK", true) {
		b.mu.Lock()
		call.H248Answered = true
		b.mu.Unlock()
		b.logger.Info("SIP-originated call media active", "call_id", call.SIPCallID, "context", call.H248ContextID, "carrier_rtp", call.H248RemoteMedia)
	}
}

func (b *Bridge) failSIPOriginatedCall(call *Call, status int, reason string) {
	if call == nil {
		return
	}
	b.mu.Lock()
	alreadyAnswered := call.SIPAnswered
	b.mu.Unlock()
	if !alreadyAnswered {
		b.sendSIPOriginatedResponse(call, status, reason, false)
	}
}

func (b *Bridge) handleSIPCancel(msg *sip.Message, responder sip.Responder) {
	b.mu.Lock()
	call := b.callsBySIP[msg.CallID()]
	b.mu.Unlock()
	if call == nil || call.Direction != "sip-to-h248" {
		_ = responder.Respond(sip.BuildResponse(msg, 481, "Call/Transaction Does Not Exist", "", "", nil))
		return
	}
	b.mu.Lock()
	call.SIPRemoteReleased = true
	b.mu.Unlock()
	_ = responder.Respond(sip.BuildResponse(msg, 200, "OK", "", "", nil))
	b.failSIPOriginatedCall(call, 487, "Request Terminated")
	b.sendCallLineEvent(call, "al/on", "onhook")
	time.AfterFunc(2*time.Second, func() { b.releaseCall(call) })
}

func (b *Bridge) startSIPInvite(ctx context.Context, call *Call) {
	if call == nil {
		return
	}
	b.mu.Lock()
	if call.Released || call.SIPCallID != "" {
		b.mu.Unlock()
		return
	}
	sender := b.sipSender
	b.mu.Unlock()
	if sender == nil {
		b.logger.Error("cannot originate SIP call: SIP server is not attached", "context", call.H248ContextID)
		return
	}
	peer, err := resolveSIPPeer(b.cfg.SIP.OutboundProxy, b.cfg.SIP.TrunkURI)
	if err != nil {
		b.logger.Error("resolve SIP IP-PBX", "context", call.H248ContextID, "error", err)
		return
	}

	requestURI := normalizeSIPURI(b.cfg.SIP.TrunkURI)
	advertised := advertisedSIPAddress(b.cfg.SIP.AdvertisedAddress, b.cfg.SIP.Listen, b.cfg.Media.RTPIP)
	localTag := randomToken("tag")
	branch := "z9hG4bK-" + randomToken("branch")
	callID := fmt.Sprintf("h248-%s-%s@%s", call.H248ContextID, randomToken("call"), hostOnly(advertised))
	caller := sanitizeSIPUser(call.CallerID)
	if caller == "" {
		caller = "anonymous"
	}
	domain := strings.TrimSpace(b.cfg.SIP.Domain)
	if domain == "" {
		domain = hostOnly(advertised)
	}
	from := fmt.Sprintf("<sip:%s@%s>;tag=%s", caller, domain, localTag)
	to := "<" + requestURI + ">"
	contact := "<sip:gateway@" + advertised + ">"
	inviteVia := "SIP/2.0/UDP " + advertised + ";branch=" + branch + ";rport"
	sdp := sip.BuildSDPWithPTime(
		"h248gw",
		b.cfg.Media.RTPIP,
		call.SIPRTPPort,
		b.cfg.Media.CodecList,
		b.cfg.Media.DTMFPayloadType,
		b.cfg.Media.PacketizationTimeMillis,
	)
	headers := map[string]string{
		"Via":                 inviteVia,
		"Max-Forwards":        "70",
		"From":                from,
		"To":                  to,
		"Call-ID":             callID,
		"CSeq":                "1 INVITE",
		"Contact":             contact,
		"Allow":               "INVITE, ACK, CANCEL, BYE, OPTIONS, INFO, UPDATE",
		"Supported":           "100rel, replaces, timer",
		"P-Asserted-Identity": fmt.Sprintf("<sip:%s@%s>", caller, domain),
		"User-Agent":          b.cfg.SIP.UserAgent,
	}
	invite := sip.BuildRequest("INVITE", requestURI, headers, sdp)

	b.mu.Lock()
	if call.Released || call.SIPCallID != "" {
		b.mu.Unlock()
		return
	}
	call.SIPCallID = callID
	call.SIPRequestURI = requestURI
	call.SIPPeer = cloneUDPAddr(peer)
	call.SIPFrom = from
	call.SIPTo = to
	call.SIPLocalTag = localTag
	call.SIPInvite = invite
	call.SIPInviteVia = inviteVia
	call.SIPInviteBranch = branch
	call.SIPInviteCSeq = 1
	call.State = "sip-invite-sent"
	b.callsBySIP[callID] = call
	b.mu.Unlock()

	if err := sender.SendTo(invite, peer); err != nil {
		b.logger.Error("send SIP INVITE", "call_id", callID, "peer", peer, "error", err)
		b.mu.Lock()
		delete(b.callsBySIP, callID)
		call.SIPCallID = ""
		call.State = "carrier-ringing"
		b.mu.Unlock()
		return
	}
	b.logger.Info("SIP INVITE sent",
		"call_id", callID,
		"context", call.H248ContextID,
		"caller", caller,
		"request_uri", requestURI,
		"peer", peer,
		"sip_rtp_port", call.SIPRTPPort,
	)
	go b.retransmitInvite(ctx, callID)
}

func (b *Bridge) retransmitInvite(ctx context.Context, callID string) {
	interval := 500 * time.Millisecond
	deadline := time.NewTimer(32 * time.Second)
	defer deadline.Stop()
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-deadline.C:
			timer.Stop()
			b.mu.Lock()
			call := b.callsBySIP[callID]
			if call != nil && !call.SIPInviteProvisional && !call.SIPAnswered && !call.Released {
				call.State = "sip-invite-timeout"
			}
			b.mu.Unlock()
			if call != nil {
				b.logger.Warn("SIP INVITE timed out", "call_id", callID)
				b.sendCallLineEvent(call, "al/on", "onhook")
				b.releaseCall(call)
			}
			return
		case <-timer.C:
		}

		b.mu.Lock()
		call := b.callsBySIP[callID]
		if call == nil || call.Released || call.SIPInviteProvisional || call.SIPAnswered {
			b.mu.Unlock()
			return
		}
		sender := b.sipSender
		invite := call.SIPInvite
		peer := cloneUDPAddr(call.SIPPeer)
		b.mu.Unlock()
		if sender != nil && peer != nil {
			if err := sender.SendTo(invite, peer); err != nil {
				b.logger.Warn("retransmit SIP INVITE", "call_id", callID, "peer", peer, "error", err)
			}
		}
		interval *= 2
		if interval > 4*time.Second {
			interval = 4 * time.Second
		}
	}
}

func (b *Bridge) handleSIPResponse(msg *sip.Message, responder sip.Responder) {
	b.mu.Lock()
	call := b.callsBySIP[msg.CallID()]
	if call == nil || call.Released {
		retained := b.retainedSIPInvites[msg.CallID()]
		b.mu.Unlock()
		if retained != nil {
			b.handleRetainedSIPInviteResponse(retained, msg, responder)
			return
		}
		b.logger.Debug("unmatched SIP response", "call_id", msg.CallID(), "status", msg.Status)
		return
	}
	method := strings.ToUpper(msg.CSeqMethod())
	if msg.Source != nil {
		call.SIPPeer = cloneUDPAddr(msg.Source)
	}
	if method == "INVITE" && msg.Status >= 100 {
		call.SIPInviteProvisional = true
	}
	if method == "INVITE" && msg.Status >= 200 {
		call.SIPInviteFinal = true
	}
	if method != "INVITE" {
		b.mu.Unlock()
		b.logger.Info("SIP transaction response", "call_id", msg.CallID(), "method", method, "status", msg.Status)
		return
	}
	peer := cloneUDPAddr(call.SIPPeer)
	b.mu.Unlock()

	if msg.Status < 200 {
		if msg.Body != "" {
			b.updateSIPRemoteMedia(call, msg.Body)
		}
		if msg.Status == 180 {
			b.mu.Lock()
			call.State = "sip-ringing"
			b.mu.Unlock()
		}
		if msg.Status == 183 {
			b.mu.Lock()
			call.State = "sip-early-media"
			b.mu.Unlock()
		}
		if rseq := msg.Header("RSeq"); rseq != "" && strings.Contains(strings.ToLower(msg.Header("Require")), "100rel") {
			b.sendPRACK(call, msg, rseq, responder)
		}
		b.logger.Info("SIP provisional response", "call_id", msg.CallID(), "status", msg.Status, "reason", msg.Reason)
		return
	}

	b.sendInviteACK(call, msg, responder, peer)
	if msg.Status >= 200 && msg.Status < 300 {
		if err := b.updateSIPRemoteMedia(call, msg.Body); err != nil {
			b.logger.Error("invalid SIP answer SDP", "call_id", msg.CallID(), "error", err)
			b.sendSIPTeardown(call, "invalid answer SDP")
			b.sendCallLineEvent(call, "al/on", "onhook")
			b.releaseCall(call)
			return
		}
		b.mu.Lock()
		call.SIPAnswered = true
		call.SIPTo = msg.Header("To")
		call.SIPRemoteTag = headerParameter(msg.Header("To"), "tag")
		call.SIPRemoteContact = firstURI(msg.Header("Contact"))
		call.State = "sip-answered"
		b.mu.Unlock()
		b.logger.Info("SIP call answered", "call_id", msg.CallID(), "status", msg.Status, "remote_rtp", call.SIPRemoteMedia)
		b.tryAnswerH248(call)
		return
	}

	b.mu.Lock()
	call.State = "sip-final-failure"
	b.mu.Unlock()
	b.retainSIPInvite(call)
	b.logger.Info("SIP INVITE failed", "call_id", msg.CallID(), "status", msg.Status, "reason", msg.Reason)
	b.sendCallLineEvent(call, "al/on", "onhook")
	time.AfterFunc(2*time.Second, func() { b.releaseCall(call) })
}

func (b *Bridge) updateSIPRemoteMedia(call *Call, body string) error {
	description, err := sip.ParseSDP(body)
	if err != nil {
		return err
	}
	remote, err := net.ResolveUDPAddr("udp", net.JoinHostPort(description.IP, strconv.Itoa(description.Port)))
	if err != nil {
		return err
	}
	b.mu.Lock()
	call.SIPRemoteMedia = cloneUDPAddr(remote)
	if description.TelephoneEventPayload > 0 {
		call.SIPTelephoneEventPT = description.TelephoneEventPayload
	}
	relay := call.Media
	telephoneEventPT := call.SIPTelephoneEventPT
	b.mu.Unlock()
	if relay != nil {
		relay.SetSIPPeer(remote, telephoneEventPT)
	}
	return nil
}

func (b *Bridge) sendInviteACK(call *Call, response *sip.Message, responder sip.Responder, peer *net.UDPAddr) {
	b.mu.Lock()
	callID := call.SIPCallID
	requestURI := call.SIPRequestURI
	from := call.SIPFrom
	inviteVia := call.SIPInviteVia
	inviteBranch := call.SIPInviteBranch
	inviteCSeq := call.SIPInviteCSeq
	b.mu.Unlock()
	b.sendInviteACKForTransaction(callID, requestURI, from, inviteVia, inviteBranch, inviteCSeq, response, responder, peer)
}

func (b *Bridge) sendInviteACKForTransaction(
	callID, requestURI, from, inviteVia, inviteBranch string,
	inviteCSeq int,
	response *sip.Message,
	responder sip.Responder,
	peer *net.UDPAddr,
) {
	if response == nil || responder == nil {
		return
	}
	via := inviteVia
	if response.Status >= 200 && response.Status < 300 {
		if contact := firstURI(response.Header("Contact")); contact != "" {
			requestURI = contact
		}
		via = "SIP/2.0/UDP " + advertisedSIPAddress(b.cfg.SIP.AdvertisedAddress, b.cfg.SIP.Listen, b.cfg.Media.RTPIP) + ";branch=z9hG4bK-" + randomToken("ack") + ";rport"
	} else if via == "" {
		branch := strings.TrimPrefix(inviteBranch, "z9hG4bK-")
		via = "SIP/2.0/UDP " + advertisedSIPAddress(b.cfg.SIP.AdvertisedAddress, b.cfg.SIP.Listen, b.cfg.Media.RTPIP) + ";branch=z9hG4bK-" + branch + ";rport"
	}
	headers := map[string]string{
		"Via":          via,
		"Max-Forwards": "70",
		"From":         from,
		"To":           response.Header("To"),
		"Call-ID":      callID,
		"CSeq":         strconv.Itoa(inviteCSeq) + " ACK",
		"User-Agent":   b.cfg.SIP.UserAgent,
	}
	ack := sip.BuildRequest("ACK", requestURI, headers, "")
	if peer == nil {
		peer = response.Source
	}
	if peer != nil {
		if err := responder.SendTo(ack, peer); err != nil {
			b.logger.Warn("send SIP INVITE ACK", "call_id", callID, "status", response.Status, "error", err)
		}
	}
}

func (b *Bridge) sendPRACK(call *Call, response *sip.Message, rseq string, responder sip.Responder) {
	b.mu.Lock()
	peer := cloneUDPAddr(call.SIPPeer)
	requestURI := call.SIPRequestURI
	if contact := firstURI(response.Header("Contact")); contact != "" {
		requestURI = contact
	}
	headers := b.dialogHeaders(call, response.Header("To"), 2, "PRACK", randomToken("prack"))
	headers["RAck"] = fmt.Sprintf("%s %d INVITE", rseq, call.SIPInviteCSeq)
	b.mu.Unlock()
	if peer != nil {
		_ = responder.SendTo(sip.BuildRequest("PRACK", requestURI, headers, ""), peer)
	}
}

func (b *Bridge) handleSIPBye(msg *sip.Message, responder sip.Responder) {
	b.mu.Lock()
	call := b.callsBySIP[msg.CallID()]
	if call != nil {
		call.SIPRemoteReleased = true
	}
	b.mu.Unlock()
	if call == nil {
		_ = responder.Respond(sip.BuildResponse(msg, 481, "Call/Transaction Does Not Exist", "", "", nil))
		return
	}
	_ = responder.Respond(sip.BuildResponse(msg, 200, "OK", "", "", nil))
	b.logger.Info("SIP BYE received", "call_id", msg.CallID())
	b.sendCallLineEvent(call, "al/on", "onhook")
	time.AfterFunc(2*time.Second, func() { b.releaseCall(call) })
}

func (b *Bridge) handleSIPReinvite(msg *sip.Message, responder sip.Responder) bool {
	b.mu.Lock()
	call := b.callsBySIP[msg.CallID()]
	b.mu.Unlock()
	if call == nil {
		return false
	}
	if msg.Body != "" {
		if err := b.updateSIPRemoteMedia(call, msg.Body); err != nil {
			_ = responder.Respond(sip.BuildResponse(msg, 488, "Not Acceptable Here", "", localContact(b.cfg), nil))
			return true
		}
	}
	sdp := sip.BuildSDPWithPTime("h248gw", b.cfg.Media.RTPIP, call.SIPRTPPort, b.cfg.Media.CodecList, call.SIPTelephoneEventPT, b.cfg.Media.PacketizationTimeMillis)
	_ = responder.Respond(sip.BuildResponse(msg, 200, "OK", sdp, localContact(b.cfg), map[string]string{"Supported": "timer"}))
	return true
}

func (b *Bridge) handleSIPSessionUpdate(msg *sip.Message, responder sip.Responder) {
	b.mu.Lock()
	call := b.callsBySIP[msg.CallID()]
	b.mu.Unlock()
	if call == nil {
		_ = responder.Respond(sip.BuildResponse(msg, 481, "Call/Transaction Does Not Exist", "", "", nil))
		return
	}
	if msg.Body != "" {
		if err := b.updateSIPRemoteMedia(call, msg.Body); err != nil {
			_ = responder.Respond(sip.BuildResponse(msg, 488, "Not Acceptable Here", "", "", nil))
			return
		}
	}
	_ = responder.Respond(sip.BuildResponse(msg, 200, "OK", "", localContact(b.cfg), nil))
}

func (b *Bridge) sendSIPTeardown(call *Call, reason string) {
	b.mu.Lock()
	if call == nil || call.SIPCallID == "" || call.SIPPeer == nil || call.SIPRemoteReleased {
		b.mu.Unlock()
		return
	}
	sender := b.sipSender
	peer := cloneUDPAddr(call.SIPPeer)
	if call.Direction == "sip-to-h248" {
		if !call.SIPAnswered {
			b.mu.Unlock()
			b.failSIPOriginatedCall(call, 503, "Carrier Call Released")
			return
		}
		requestURI := call.SIPRemoteContact
		if requestURI == "" {
			requestURI = firstURI(call.SIPFrom)
		}
		headers := map[string]string{
			"Via":          "SIP/2.0/UDP " + advertisedSIPAddress(b.cfg.SIP.AdvertisedAddress, b.cfg.SIP.Listen, b.cfg.Media.RTPIP) + ";branch=z9hG4bK-" + randomToken("bye") + ";rport",
			"Max-Forwards": "70",
			"From":         call.SIPTo,
			"To":           call.SIPFrom,
			"Call-ID":      call.SIPCallID,
			"CSeq":         "1 BYE",
			"User-Agent":   b.cfg.SIP.UserAgent,
		}
		call.State = "sip-bye-sent"
		b.mu.Unlock()
		if sender != nil && requestURI != "" {
			if err := sender.SendTo(sip.BuildRequest("BYE", requestURI, headers, ""), peer); err != nil {
				b.logger.Warn("send SIP teardown", "call_id", call.SIPCallID, "method", "BYE", "error", err)
			}
		}
		return
	}
	method := "CANCEL"
	requestURI := call.SIPRequestURI
	cseq := call.SIPInviteCSeq
	to := call.SIPTo
	branch := strings.TrimPrefix(call.SIPInviteBranch, "z9hG4bK-")
	if !call.SIPAnswered && call.SIPInviteFinal {
		call.State = "sip-final-complete"
		b.mu.Unlock()
		b.logger.Info("SIP CANCEL skipped after final INVITE response", "call_id", call.SIPCallID, "reason", reason)
		return
	}
	if call.SIPAnswered {
		method = "BYE"
		cseq++
		branch = randomToken("bye")
		if call.SIPRemoteContact != "" {
			requestURI = call.SIPRemoteContact
		}
	}
	headers := b.dialogHeaders(call, to, cseq, method, branch)
	retainInvite := method == "CANCEL"
	if retainInvite && call.SIPInviteVia != "" {
		// CANCEL belongs to the INVITE client transaction and therefore reuses
		// the exact top Via value, including its branch parameter.
		headers["Via"] = call.SIPInviteVia
	}
	call.State = "sip-" + strings.ToLower(method) + "-sent"
	b.mu.Unlock()
	if retainInvite {
		b.retainSIPInvite(call)
	}
	if sender == nil {
		return
	}
	request := sip.BuildRequest(method, requestURI, headers, "")
	if err := sender.SendTo(request, peer); err != nil {
		b.logger.Warn("send SIP teardown", "call_id", call.SIPCallID, "method", method, "error", err)
		return
	}
	b.logger.Info("SIP teardown sent", "call_id", call.SIPCallID, "method", method, "reason", reason)
}

func (b *Bridge) retainSIPInvite(call *Call) {
	if call == nil {
		return
	}
	b.mu.Lock()
	if call.SIPCallID == "" || call.SIPRequestURI == "" || call.SIPInviteCSeq <= 0 {
		b.mu.Unlock()
		return
	}
	ttl := b.sipInviteTransactionTTL
	if ttl <= 0 {
		ttl = defaultSIPInviteTransactionTTL
	}
	retained := b.retainedSIPInvites[call.SIPCallID]
	if retained == nil {
		retained = &retainedSIPInvite{callID: call.SIPCallID}
		b.retainedSIPInvites[call.SIPCallID] = retained
	}
	retained.requestURI = call.SIPRequestURI
	retained.from = call.SIPFrom
	retained.inviteVia = call.SIPInviteVia
	retained.inviteBranch = call.SIPInviteBranch
	retained.inviteCSeq = call.SIPInviteCSeq
	retained.peer = cloneUDPAddr(call.SIPPeer)
	retained.expiresAt = time.Now().Add(ttl)
	b.mu.Unlock()
	b.scheduleRetainedSIPInviteExpiry(retained, ttl)
}

func (b *Bridge) refreshRetainedSIPInvite(retained *retainedSIPInvite) {
	if retained == nil {
		return
	}
	b.mu.Lock()
	if b.retainedSIPInvites[retained.callID] != retained {
		b.mu.Unlock()
		return
	}
	ttl := b.sipInviteTransactionTTL
	if ttl <= 0 {
		ttl = defaultSIPInviteTransactionTTL
	}
	retained.expiresAt = time.Now().Add(ttl)
	b.mu.Unlock()
	b.scheduleRetainedSIPInviteExpiry(retained, ttl)
}

func (b *Bridge) scheduleRetainedSIPInviteExpiry(retained *retainedSIPInvite, ttl time.Duration) {
	time.AfterFunc(ttl, func() {
		b.mu.Lock()
		current := b.retainedSIPInvites[retained.callID]
		expired := current == retained && !time.Now().Before(retained.expiresAt)
		if expired {
			delete(b.retainedSIPInvites, retained.callID)
		}
		b.mu.Unlock()
		if expired {
			b.logger.Debug("expired retained SIP INVITE transaction", "call_id", retained.callID)
		}
	})
}

func (b *Bridge) handleRetainedSIPInviteResponse(retained *retainedSIPInvite, msg *sip.Message, responder sip.Responder) {
	if retained == nil || msg == nil {
		return
	}
	method := strings.ToUpper(msg.CSeqMethod())
	cseq := parseCSeqNumber(msg.CSeqNumber())
	b.mu.Lock()
	if b.retainedSIPInvites[retained.callID] != retained {
		b.mu.Unlock()
		return
	}
	if msg.Source != nil {
		retained.peer = cloneUDPAddr(msg.Source)
	}
	peer := cloneUDPAddr(retained.peer)
	callID := retained.callID
	requestURI := retained.requestURI
	from := retained.from
	inviteVia := retained.inviteVia
	inviteBranch := retained.inviteBranch
	inviteCSeq := retained.inviteCSeq
	b.mu.Unlock()

	switch method {
	case "CANCEL":
		if cseq != inviteCSeq {
			b.logger.Debug("unmatched retained SIP CANCEL response", "call_id", callID, "cseq", cseq, "status", msg.Status)
			return
		}
		b.logger.Info("SIP CANCEL transaction response", "call_id", callID, "status", msg.Status)
		return
	case "BYE":
		if cseq == inviteCSeq+1 {
			b.logger.Info("SIP late-answer BYE response", "call_id", callID, "status", msg.Status)
			return
		}
	case "INVITE":
		if cseq != inviteCSeq {
			b.logger.Debug("unmatched retained SIP INVITE response", "call_id", callID, "cseq", cseq, "status", msg.Status)
			return
		}
		if msg.Status < 200 {
			b.logger.Debug("late provisional response for canceled SIP INVITE", "call_id", callID, "status", msg.Status)
			return
		}
		b.sendInviteACKForTransaction(callID, requestURI, from, inviteVia, inviteBranch, inviteCSeq, msg, responder, peer)
		b.refreshRetainedSIPInvite(retained)
		if msg.Status < 300 {
			b.sendLateAnswerBYE(retained, msg, responder)
		}
		b.logger.Info("SIP final response matched retained INVITE", "call_id", callID, "status", msg.Status)
		return
	}
	b.logger.Debug("unmatched response for retained SIP INVITE", "call_id", callID, "method", method, "status", msg.Status)
}

func (b *Bridge) sendLateAnswerBYE(retained *retainedSIPInvite, response *sip.Message, responder sip.Responder) {
	if retained == nil || response == nil || responder == nil {
		return
	}
	b.mu.Lock()
	if b.retainedSIPInvites[retained.callID] != retained || retained.late2xxBYESent {
		b.mu.Unlock()
		return
	}
	retained.late2xxBYESent = true
	callID := retained.callID
	requestURI := retained.requestURI
	if contact := firstURI(response.Header("Contact")); contact != "" {
		requestURI = contact
	}
	from := retained.from
	inviteCSeq := retained.inviteCSeq
	peer := cloneUDPAddr(retained.peer)
	b.mu.Unlock()
	if peer == nil {
		peer = response.Source
	}
	if peer == nil || requestURI == "" {
		return
	}
	headers := map[string]string{
		"Via":          "SIP/2.0/UDP " + advertisedSIPAddress(b.cfg.SIP.AdvertisedAddress, b.cfg.SIP.Listen, b.cfg.Media.RTPIP) + ";branch=z9hG4bK-" + randomToken("late-bye") + ";rport",
		"Max-Forwards": "70",
		"From":         from,
		"To":           response.Header("To"),
		"Call-ID":      callID,
		"CSeq":         strconv.Itoa(inviteCSeq+1) + " BYE",
		"User-Agent":   b.cfg.SIP.UserAgent,
	}
	if err := responder.SendTo(sip.BuildRequest("BYE", requestURI, headers, ""), peer); err != nil {
		b.mu.Lock()
		if b.retainedSIPInvites[retained.callID] == retained {
			retained.late2xxBYESent = false
		}
		b.mu.Unlock()
		b.logger.Warn("send SIP BYE for late INVITE answer", "call_id", callID, "error", err)
		return
	}
	b.logger.Info("late SIP INVITE answer acknowledged and cleared", "call_id", callID)
}

func (b *Bridge) dialogHeaders(call *Call, to string, cseq int, method, branch string) map[string]string {
	if strings.HasPrefix(branch, "z9hG4bK-") {
		branch = strings.TrimPrefix(branch, "z9hG4bK-")
	}
	return map[string]string{
		"Via":          "SIP/2.0/UDP " + advertisedSIPAddress(b.cfg.SIP.AdvertisedAddress, b.cfg.SIP.Listen, b.cfg.Media.RTPIP) + ";branch=z9hG4bK-" + branch + ";rport",
		"Max-Forwards": "70",
		"From":         call.SIPFrom,
		"To":           to,
		"Call-ID":      call.SIPCallID,
		"CSeq":         fmt.Sprintf("%d %s", cseq, method),
		"User-Agent":   b.cfg.SIP.UserAgent,
	}
}

func resolveSIPPeer(outboundProxy, requestURI string) (*net.UDPAddr, error) {
	target := strings.TrimSpace(outboundProxy)
	if target == "" {
		target = requestURI
		if index := strings.Index(target, ":"); index >= 0 {
			target = target[index+1:]
		}
		if at := strings.LastIndex(target, "@"); at >= 0 {
			target = target[at+1:]
		}
		if end := strings.IndexAny(target, ";?>"); end >= 0 {
			target = target[:end]
		}
	}
	target = strings.Trim(strings.TrimSpace(target), "<>")
	if strings.HasPrefix(strings.ToLower(target), "sip:") {
		target = target[4:]
	}
	if at := strings.LastIndex(target, "@"); at >= 0 {
		target = target[at+1:]
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		if strings.Count(target, ":") == 0 {
			target = net.JoinHostPort(target, "5060")
		} else if net.ParseIP(strings.Trim(target, "[]")) != nil {
			target = net.JoinHostPort(strings.Trim(target, "[]"), "5060")
		}
	}
	address, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", target, err)
	}
	return address, nil
}

func normalizeSIPURI(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "<>")
	if !strings.HasPrefix(strings.ToLower(value), "sip:") {
		value = "sip:" + value
	}
	return value
}

func advertisedSIPAddress(configured, listen, fallbackIP string) string {
	value := strings.TrimSpace(configured)
	if value == "" {
		value = strings.TrimSpace(listen)
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		host = strings.Trim(value, "[]")
		port = "5060"
	}
	parsed := net.ParseIP(strings.Trim(host, "[]"))
	if host == "" || parsed != nil && parsed.IsUnspecified() {
		host = fallbackIP
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), port)
}

func hostOnly(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(address, "[]")
}

func sanitizeSIPUser(value string) string {
	var result strings.Builder
	for _, character := range strings.TrimSpace(value) {
		if character >= '0' && character <= '9' || character == '+' || character == '*' || character == '#' {
			result.WriteRune(character)
		}
	}
	return result.String()
}

func randomToken(prefix string) string {
	return fmt.Sprintf("%s-%x", prefix, time.Now().UnixNano())
}

func headerParameter(value, name string) string {
	for _, part := range strings.Split(value, ";") {
		key, parameter, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.EqualFold(key, name) {
			return strings.Trim(parameter, `"`)
		}
	}
	return ""
}

func firstURI(value string) string {
	value = strings.TrimSpace(value)
	if start := strings.Index(value, "<"); start >= 0 {
		if end := strings.Index(value[start+1:], ">"); end >= 0 {
			return value[start+1 : start+1+end]
		}
	}
	if end := strings.IndexAny(value, ";,"); end >= 0 {
		value = value[:end]
	}
	return strings.TrimSpace(value)
}

func sipURIUser(uri string) string {
	uri = strings.Trim(strings.TrimSpace(uri), "<>")
	if index := strings.Index(uri, ":"); index >= 0 {
		uri = uri[index+1:]
	}
	if index := strings.Index(uri, "@"); index >= 0 {
		uri = uri[:index]
	}
	if index := strings.IndexAny(uri, ";?"); index >= 0 {
		uri = uri[:index]
	}
	return strings.TrimSpace(uri)
}

func containsCodec(codecs []string, want string) bool {
	for _, codec := range codecs {
		if strings.EqualFold(codec, want) {
			return true
		}
	}
	return false
}

func addHeaderParameter(value, name, parameter string) string {
	if headerParameter(value, name) != "" {
		return value
	}
	return value + ";" + name + "=" + parameter
}

func parseCSeqNumber(value string) int {
	number, err := strconv.Atoi(value)
	if err != nil || number <= 0 {
		return 1
	}
	return number
}
