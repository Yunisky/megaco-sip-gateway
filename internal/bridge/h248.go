package bridge

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Yunisky/megaco-sip-gateway/internal/h248"
)

func (b *Bridge) HandleH248(ctx context.Context, msg *h248.Message, responder h248.Responder) {
	if msg.TransactionType != "Transaction" {
		return
	}
	if isIncomingCallAdd(msg, b.cfg.H248.PhysicalTermination) {
		b.mu.Lock()
		outbound := b.outboundCall
		if outbound != nil && (outbound.Released || !outbound.H248DigitsSent) {
			outbound = nil
		}
		b.mu.Unlock()
		if outbound != nil {
			b.handleOutboundCallAdd(msg, responder, outbound)
			return
		}
		b.handleIncomingCallAdd(ctx, msg, responder)
		return
	}

	actions, err := simpleActionReplies(msg, b.cfg.H248.Version)
	if err != nil {
		_ = responder.Respond(h248.BuildErrorReply(msg, b.cfg.H248.MGID, "400", err.Error()))
		return
	}
	if err := responder.Respond(h248.BuildActionReply(msg, b.cfg.H248.MGID, actions)); err != nil {
		b.logger.Warn("send H.248 transaction reply", "transaction", msg.TransactionID, "error", err)
		return
	}

	for _, command := range msg.Commands {
		b.processH248Command(ctx, msg, command, responder)
	}
}

func (b *Bridge) HandleH248Ready(ctx context.Context, responder h248.Responder) {
	b.mu.Lock()
	b.line.ready = true
	b.line.readyAt = time.Now()
	b.line.responder = responder
	requestID := b.line.eventRequestID
	b.mu.Unlock()
	b.logger.Info("H.248 physical termination active", "termination", b.cfg.H248.PhysicalTermination, "mgc", responder.RemoteAddr())
	if requestID != "" {
		b.sendInitialOnhook(ctx)
	}
}

func (b *Bridge) HandleH248Unavailable(_ context.Context, remote *net.UDPAddr) {
	b.mu.Lock()
	b.line.ready = false
	b.line.readyAt = time.Time{}
	b.line.eventRequestID = ""
	b.line.initialOnhookSent = false
	b.line.responder = nil
	b.pendingH248 = make(map[string]pendingH248Transaction)
	calls := make([]*Call, 0, len(b.callsByH248)+1)
	seen := make(map[*Call]struct{})
	for _, call := range b.callsByH248 {
		if _, exists := seen[call]; !exists {
			seen[call] = struct{}{}
			calls = append(calls, call)
		}
	}
	if b.outboundCall != nil {
		if _, exists := seen[b.outboundCall]; !exists {
			calls = append(calls, b.outboundCall)
		}
	}
	b.mu.Unlock()
	b.logger.Error("H.248 line unavailable", "mgc", remote, "active_calls", len(calls))
	for _, call := range calls {
		b.sendSIPTeardown(call, "H.248 MGC unavailable")
		b.releaseCall(call)
	}
}

func (b *Bridge) HandleH248Response(_ context.Context, msg *h248.Message, _ h248.Responder) {
	b.mu.Lock()
	pending, found := b.pendingH248[msg.TransactionID]
	if found && msg.TransactionType == "Reply" {
		delete(b.pendingH248, msg.TransactionID)
	}
	var call *Call
	if found && pending.callID != "" {
		call = b.callsBySIP[pending.callID]
		if call == nil {
			for _, candidate := range b.callsByH248 {
				if candidate.ID == pending.callID {
					call = candidate
					break
				}
			}
		}
		if call == nil && b.outboundCall != nil && b.outboundCall.ID == pending.callID {
			call = b.outboundCall
		}
	}
	if msg.ErrorCode != "" && call != nil && pending.phase == "answer" {
		call.H248Answered = false
	}
	originationFailed := msg.ErrorCode != "" && call != nil && strings.HasPrefix(pending.phase, "origination-")
	b.mu.Unlock()
	if found {
		b.logger.Info("H.248 generated transaction response",
			"transaction", msg.TransactionID,
			"phase", pending.phase,
			"type", msg.TransactionType,
			"error_code", msg.ErrorCode,
		)
	}
	if originationFailed {
		b.failSIPOriginatedCall(call, 503, "H248 Origination Rejected")
		b.releaseCall(call)
	}
}

func (b *Bridge) handleIncomingCallAdd(ctx context.Context, msg *h248.Message, responder h248.Responder) {
	remoteMedia, ok := h248.ExtractRemoteMedia(msg.Raw)
	if !ok {
		_ = responder.Respond(h248.BuildErrorReply(msg, b.cfg.H248.MGID, "510", "missing remote RTP descriptor"))
		return
	}
	remoteRTP, err := net.ResolveUDPAddr("udp", net.JoinHostPort(remoteMedia.IP, strconv.Itoa(remoteMedia.Port)))
	if err != nil {
		_ = responder.Respond(h248.BuildErrorReply(msg, b.cfg.H248.MGID, "510", "invalid remote RTP address"))
		return
	}

	b.mu.Lock()
	contextID, ephemeral, h248Port, sipPort := b.allocateCallIdentifiersLocked()
	call := &Call{
		ID:                   "h248-" + contextID,
		Direction:            "h248-to-sip",
		State:                "h248-add-received",
		CreatedAt:            time.Now(),
		H248ContextID:        contextID,
		H248ContextAllocated: true,
		H248PhysicalTerm:     b.cfg.H248.PhysicalTermination,
		H248EphemeralTerm:    ephemeral,
		H248EventRequestID:   h248.EventRequestID(msg.Raw),
		H248Responder:        responder,
		H248RemoteMedia:      cloneUDPAddr(remoteRTP),
		H248RTPPort:          h248Port,
		H248TelephoneEventPT: remoteMedia.TelephoneEventPayload,
		SIPRTPPort:           sipPort,
		SIPTelephoneEventPT:  b.cfg.Media.DTMFPayloadType,
	}
	if call.H248TelephoneEventPT == 0 {
		call.H248TelephoneEventPT = b.cfg.Media.H248DTMFPayloadType
	}
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
		b.logger.Error("allocate RTP relay", "context", contextID, "error", err)
		_ = responder.Respond(h248.BuildErrorReply(msg, b.cfg.H248.MGID, "510", "RTP resource unavailable"))
		return
	}
	relay.SetH248Peer(remoteRTP, call.H248TelephoneEventPT)
	call.Media = relay

	b.mu.Lock()
	b.callsByH248[contextID] = call
	b.mu.Unlock()

	reply := buildIncomingAddReply(
		msg,
		b.cfg.H248.MGID,
		contextID,
		call.H248PhysicalTerm,
		call.H248EphemeralTerm,
		b.cfg.Media.H248RTPIP,
		call.H248RTPPort,
		call.H248TelephoneEventPT,
		b.cfg.Media.PacketizationTimeMillis,
	)
	if err := responder.Respond(reply); err != nil {
		b.logger.Warn("send H.248 Add reply", "context", contextID, "error", err)
		b.releaseCall(call)
		return
	}
	b.mu.Lock()
	call.State = "carrier-ringing"
	b.mu.Unlock()
	b.logger.Info("H.248 incoming call allocated",
		"context", contextID,
		"physical_termination", call.H248PhysicalTerm,
		"ephemeral_termination", call.H248EphemeralTerm,
		"carrier_rtp", remoteRTP,
		"local_h248_rtp_port", call.H248RTPPort,
		"local_sip_rtp_port", call.SIPRTPPort,
	)
}

func (b *Bridge) handleOutboundCallAdd(msg *h248.Message, responder h248.Responder, call *Call) {
	if call == nil {
		_ = responder.Respond(h248.BuildErrorReply(msg, b.cfg.H248.MGID, "510", "no pending origination"))
		return
	}
	b.mu.Lock()
	if call.Released || call.H248ContextAllocated {
		b.mu.Unlock()
		_ = responder.Respond(h248.BuildErrorReply(msg, b.cfg.H248.MGID, "510", "origination context already allocated"))
		return
	}
	call.H248ContextAllocated = true
	call.H248Responder = responder
	if requestID := h248.EventRequestID(msg.Raw); requestID != "" {
		call.H248EventRequestID = requestID
	}
	call.State = "h248-outbound-context-allocated"
	b.callsByH248[call.H248ContextID] = call
	b.mu.Unlock()

	if b.cfg.Media.H248DTMFMode == "inband" {
		b.mu.Lock()
		call.H248TelephoneEventPT = 0
		b.mu.Unlock()
		b.logger.Info("H.248 outbound DTMF mode selected",
			"context", call.H248ContextID,
			"mode", "inband",
		)
	} else if offeredDTMF, ok := h248.ExtractLocalTelephoneEventPayload(msg.Raw); ok {
		b.mu.Lock()
		call.H248TelephoneEventPT = offeredDTMF
		relay := call.Media
		b.mu.Unlock()
		if relay != nil {
			relay.SetH248DTMFPayloadType(offeredDTMF)
		}
		b.logger.Info("H.248 outbound DTMF payload negotiated",
			"context", call.H248ContextID,
			"telephone_event_payload", offeredDTMF,
		)
	}

	if remoteMedia, ok := h248.ExtractRemoteMedia(msg.Raw); ok {
		if remote, err := net.ResolveUDPAddr("udp", net.JoinHostPort(remoteMedia.IP, strconv.Itoa(remoteMedia.Port))); err == nil {
			b.mu.Lock()
			call.H248RemoteMedia = cloneUDPAddr(remote)
			if remoteMedia.TelephoneEventPayload > 0 {
				call.H248TelephoneEventPT = remoteMedia.TelephoneEventPayload
			}
			relay := call.Media
			h248DTMF := call.H248TelephoneEventPT
			b.mu.Unlock()
			if relay != nil {
				relay.SetH248Peer(remote, h248DTMF)
			}
		}
	}

	reply := buildIncomingAddReply(
		msg,
		b.cfg.H248.MGID,
		call.H248ContextID,
		call.H248PhysicalTerm,
		call.H248EphemeralTerm,
		b.cfg.Media.H248RTPIP,
		call.H248RTPPort,
		call.H248TelephoneEventPT,
		b.cfg.Media.PacketizationTimeMillis,
	)
	if err := responder.Respond(reply); err != nil {
		b.failSIPOriginatedCall(call, 503, "Carrier Context Allocation Failed")
		b.releaseCall(call)
		return
	}
	b.sendSIPOriginatedResponse(call, 183, "Session Progress", true)
	b.logger.Info("H.248 outbound context allocated",
		"context", call.H248ContextID,
		"destination", call.DestinationNumber,
		"ephemeral_termination", call.H248EphemeralTerm,
		"h248_rtp_port", call.H248RTPPort,
	)
}

func (b *Bridge) scheduleOriginationOffhook(call *Call) {
	b.mu.Lock()
	if call == nil || call.Released || call.H248OffhookSent {
		b.mu.Unlock()
		return
	}
	notBefore := b.line.readyAt.Add(time.Duration(b.cfg.H248.OriginationStabilizationSeconds) * time.Second)
	delay := time.Until(notBefore)
	if delay < 0 {
		delay = 0
	}
	b.mu.Unlock()
	time.AfterFunc(delay, func() { b.sendOriginationOffhook(call) })
}

func (b *Bridge) sendOriginationOffhook(call *Call) {
	b.mu.Lock()
	if call == nil || call.Released || call.H248OffhookSent || !b.line.ready || !b.line.initialOnhookSent || b.line.eventRequestID == "" || b.line.responder == nil {
		b.mu.Unlock()
		b.failSIPOriginatedCall(call, 503, "H248 Line Not Ready")
		b.releaseCall(call)
		return
	}
	tid := b.newH248TransactionID()
	requestID := b.line.eventRequestID
	responder := b.line.responder
	call.H248Responder = responder
	call.H248EventRequestID = requestID
	call.H248OffhookSent = true
	call.State = "h248-offhook-sent"
	b.pendingH248[tid] = pendingH248Transaction{callID: call.ID, phase: "origination-offhook"}
	b.mu.Unlock()

	message := h248.BuildNotify(b.cfg.H248.Version, b.cfg.H248.MGID, tid, "-", b.cfg.H248.PhysicalTermination, requestID, time.Now(), "al/of")
	if err := responder.SendTo(message, responder.RemoteAddr()); err != nil {
		b.mu.Lock()
		call.H248OffhookSent = false
		delete(b.pendingH248, tid)
		b.mu.Unlock()
		b.failSIPOriginatedCall(call, 503, "H248 Offhook Failed")
		b.releaseCall(call)
		return
	}
	b.logger.Info("H.248 origination off-hook sent", "call_id", call.SIPCallID, "destination", call.DestinationNumber, "transaction", tid)
}

func (b *Bridge) scheduleOriginationDigits(call *Call) {
	b.mu.Lock()
	if call == nil || call.Released || call.H248DigitsSent || call.H248DigitsScheduled {
		b.mu.Unlock()
		return
	}
	call.H248DigitsScheduled = true
	delay := time.Duration(b.cfg.H248.DigitReportDelayMilliseconds) * time.Millisecond
	b.mu.Unlock()
	time.AfterFunc(delay, func() { b.sendOriginationDigits(call) })
}

func (b *Bridge) sendOriginationDigits(call *Call) {
	b.mu.Lock()
	if call == nil || call.Released || call.H248DigitsSent || call.H248Responder == nil || call.H248EventRequestID == "" {
		if call != nil {
			call.H248DigitsScheduled = false
		}
		b.mu.Unlock()
		return
	}
	tid := b.newH248TransactionID()
	responder := call.H248Responder
	requestID := call.H248EventRequestID
	number := call.DestinationNumber
	call.H248DigitsScheduled = false
	call.H248DigitsSent = true
	call.State = "h248-digits-sent"
	b.pendingH248[tid] = pendingH248Transaction{callID: call.ID, phase: "origination-digits"}
	b.mu.Unlock()

	message := h248.BuildDigitMapCompleteNotify(b.cfg.H248.Version, b.cfg.H248.MGID, tid, "-", b.cfg.H248.PhysicalTermination, requestID, time.Now(), number)
	if err := responder.SendTo(message, responder.RemoteAddr()); err != nil {
		b.mu.Lock()
		call.H248DigitsSent = false
		delete(b.pendingH248, tid)
		b.mu.Unlock()
		b.failSIPOriginatedCall(call, 503, "H248 Digit Report Failed")
		b.releaseCall(call)
		return
	}
	b.logger.Info("H.248 digit map completion sent", "call_id", call.SIPCallID, "destination", number, "transaction", tid)
}

func (b *Bridge) processH248Command(ctx context.Context, msg *h248.Message, command h248.Command, responder h248.Responder) {
	requestID := h248.EventRequestID(command.Body)
	if requestID == "" {
		requestID = h248.EventRequestID(msg.Raw)
	}
	if isLineTermination(command.TerminationID, b.cfg.H248.PhysicalTermination) && (command.ContextID == "" || command.ContextID == "-") {
		if requestID != "" {
			b.mu.Lock()
			b.line.eventRequestID = requestID
			b.line.responder = responder
			ready := b.line.ready
			b.mu.Unlock()
			if ready {
				b.sendInitialOnhook(ctx)
			}
		}
		b.mu.Lock()
		outbound := b.outboundCall
		if outbound != nil && outbound.H248OffhookSent && !outbound.H248DigitsSent && requestID != "" {
			outbound.H248EventRequestID = requestID
			outbound.H248Responder = responder
		}
		shouldReportDigits := outbound != nil && outbound.H248OffhookSent && !outbound.H248DigitsSent &&
			(strings.Contains(strings.ToLower(command.Body), "dd/ce") || strings.Contains(strings.ToLower(command.Body), "cg/dt"))
		b.mu.Unlock()
		if shouldReportDigits {
			b.scheduleOriginationDigits(outbound)
		}
		return
	}

	contextID := command.ContextID
	if contextID == "" {
		contextID = msg.ContextID
	}
	call := b.findCallByContext(contextID)
	if call == nil {
		if command.Name == "Subtract" && (contextID == "*" || command.TerminationID == "*") {
			b.releaseAllCallsFromCarrier()
		}
		return
	}

	b.mu.Lock()
	call.H248Responder = responder
	if requestID != "" && isLineTermination(command.TerminationID, call.H248PhysicalTerm) {
		call.H248EventRequestID = requestID
	}
	if callerID := h248.DecodeAndispCallerID(command.Body); callerID != "" {
		call.CallerID = callerID
	}
	b.mu.Unlock()

	mediaActivated := false
	if media, ok := h248.ExtractRemoteMedia(command.Body); ok {
		if remote, err := net.ResolveUDPAddr("udp", net.JoinHostPort(media.IP, strconv.Itoa(media.Port))); err == nil {
			b.mu.Lock()
			call.H248RemoteMedia = cloneUDPAddr(remote)
			if media.TelephoneEventPayload > 0 {
				call.H248TelephoneEventPT = media.TelephoneEventPayload
			}
			relay := call.Media
			telephoneEventPT := call.H248TelephoneEventPT
			b.mu.Unlock()
			if relay != nil {
				relay.SetH248Peer(remote, telephoneEventPT)
			}
			mediaActivated = strings.Contains(strings.ToLower(command.Body), "mo=sr")
		}
	}
	if call.Direction == "sip-to-h248" && mediaActivated {
		b.answerSIPOriginatedCall(call)
	}

	if command.Name == "Modify" && isLineTermination(command.TerminationID, call.H248PhysicalTerm) && h248.ContainsRingSignal(command.Body) {
		b.startSIPInvite(ctx, call)
	}

	if command.Name == "Subtract" || isCarrierClear(command) {
		b.handleCarrierRelease(call, command.Name == "Subtract")
		return
	}
	if requestID != "" {
		b.tryAnswerH248(call)
	}
}

func (b *Bridge) sendInitialOnhook(_ context.Context) {
	b.mu.Lock()
	if !b.line.ready || b.line.initialOnhookSent || b.line.eventRequestID == "" || b.line.responder == nil {
		b.mu.Unlock()
		return
	}
	tid := b.newH248TransactionID()
	requestID := b.line.eventRequestID
	responder := b.line.responder
	b.line.initialOnhookSent = true
	b.pendingH248[tid] = pendingH248Transaction{phase: "initial-onhook"}
	b.mu.Unlock()

	message := h248.BuildNotify(
		b.cfg.H248.Version,
		b.cfg.H248.MGID,
		tid,
		"-",
		b.cfg.H248.PhysicalTermination,
		requestID,
		time.Now(),
		"al/on",
	)
	if err := responder.SendTo(message, responder.RemoteAddr()); err != nil {
		b.mu.Lock()
		b.line.initialOnhookSent = false
		delete(b.pendingH248, tid)
		b.mu.Unlock()
		b.logger.Warn("send initial H.248 on-hook", "error", err)
		return
	}
	b.logger.Info("initial H.248 on-hook sent", "transaction", tid, "request_id", requestID)
}

func (b *Bridge) tryAnswerH248(call *Call) {
	b.sendCallLineEvent(call, "al/of", "answer")
}

func (b *Bridge) sendCallLineEvent(call *Call, event, phase string) bool {
	b.mu.Lock()
	if call == nil || call.Released || call.H248Responder == nil || call.H248EventRequestID == "" {
		b.mu.Unlock()
		return false
	}
	if phase == "answer" {
		if !call.SIPAnswered || call.H248Answered {
			b.mu.Unlock()
			return false
		}
		call.H248Answered = true
	}
	if phase == "onhook" {
		if call.H248OnhookSent {
			b.mu.Unlock()
			return false
		}
		call.H248OnhookSent = true
	}
	tid := b.newH248TransactionID()
	responder := call.H248Responder
	requestID := call.H248EventRequestID
	contextID := call.H248ContextID
	if !call.H248ContextAllocated {
		contextID = "-"
	}
	termination := call.H248PhysicalTerm
	b.pendingH248[tid] = pendingH248Transaction{callID: call.ID, phase: phase}
	b.mu.Unlock()

	message := h248.BuildNotify(b.cfg.H248.Version, b.cfg.H248.MGID, tid, contextID, termination, requestID, time.Now(), event)
	if err := responder.SendTo(message, responder.RemoteAddr()); err != nil {
		b.mu.Lock()
		delete(b.pendingH248, tid)
		if phase == "answer" {
			call.H248Answered = false
		}
		if phase == "onhook" {
			call.H248OnhookSent = false
		}
		b.mu.Unlock()
		b.logger.Warn("send H.248 line event", "call", callLogID(call), "event", event, "error", err)
		return false
	}
	b.logger.Info("H.248 line event sent", "call", callLogID(call), "context", contextID, "event", event, "transaction", tid)
	return true
}

func (b *Bridge) handleCarrierRelease(call *Call, contextReleased bool) {
	if call == nil {
		return
	}
	b.mu.Lock()
	needsOnhook := !call.H248OnhookSent && (call.H248OffhookSent || call.H248Answered)
	if contextReleased {
		// A Subtract has already returned the physical termination to the null
		// Context. Report the final on-hook there instead of referencing the
		// deleted call Context.
		call.H248ContextAllocated = false
		if call.H248EventRequestID != "" && call.H248Responder != nil {
			b.line.eventRequestID = call.H248EventRequestID
			b.line.responder = call.H248Responder
		}
	}
	b.mu.Unlock()
	if needsOnhook {
		b.sendCallLineEvent(call, "al/on", "onhook")
	}
	b.sendSIPTeardown(call, "carrier release")
	b.releaseCall(call)
}

func (b *Bridge) releaseAllCallsFromCarrier() {
	b.mu.Lock()
	calls := make([]*Call, 0, len(b.callsByH248))
	for _, call := range b.callsByH248 {
		calls = append(calls, call)
	}
	b.mu.Unlock()
	for _, call := range calls {
		b.handleCarrierRelease(call, true)
	}
}

func isIncomingCallAdd(msg *h248.Message, physicalTermination string) bool {
	if msg == nil {
		return false
	}
	foundPhysical := false
	foundEphemeral := false
	contextID := ""
	for _, command := range msg.Commands {
		if command.Name != "Add" {
			continue
		}
		if contextID == "" {
			contextID = command.ContextID
		}
		if strings.EqualFold(command.TerminationID, physicalTermination) {
			foundPhysical = true
		}
		if command.TerminationID == "$" {
			foundEphemeral = true
		}
	}
	return contextID == "$" && foundPhysical && foundEphemeral
}

func simpleActionReplies(msg *h248.Message, version int) ([]h248.ActionReply, error) {
	actions := make([]h248.ActionReply, 0, 2)
	indices := make(map[string]int)
	for _, command := range msg.Commands {
		if command.TerminationID == "" {
			return nil, fmt.Errorf("%s command has no termination ID", command.Name)
		}
		contextID := command.ContextID
		if contextID == "" {
			contextID = msg.ContextID
		}
		if contextID == "" {
			contextID = "-"
		}
		text := h248.SimpleCommandReply(command)
		if command.Name == "ServiceChange" {
			prefix := "SC"
			text = fmt.Sprintf("%s=%s{SV{V=%d}}", prefix, command.TerminationID, version)
		}
		index, exists := indices[contextID]
		if !exists {
			indices[contextID] = len(actions)
			actions = append(actions, h248.ActionReply{ContextID: contextID})
			index = len(actions) - 1
		}
		actions[index].Commands = append(actions[index].Commands, text)
	}
	if len(actions) == 0 {
		return nil, fmt.Errorf("transaction contains no supported commands")
	}
	return actions, nil
}

func buildIncomingAddReply(req *h248.Message, mid, contextID, physicalTermination, ephemeralTermination, rtpIP string, rtpPort, dtmfPayload, ptime int) string {
	if ptime <= 0 {
		ptime = 20
	}
	media := ""
	if dtmfPayload > 0 && dtmfPayload < 128 {
		media = fmt.Sprintf("A=%s{M{L{v=0\r\nc=IN IP4 %s\r\nm=audio %d RTP/AVP 8 %d\r\na=ptime:%d\r\na=rtpmap:%d telephone-event/8000\r\na=fmtp:%d 0-15\r\n}}}",
			ephemeralTermination, rtpIP, rtpPort, dtmfPayload, ptime, dtmfPayload, dtmfPayload)
	} else {
		media = fmt.Sprintf("A=%s{M{L{v=0\r\nc=IN IP4 %s\r\nm=audio %d RTP/AVP 8\r\na=ptime:%d\r\n}}}",
			ephemeralTermination, rtpIP, rtpPort, ptime)
	}
	return h248.BuildActionReply(req, mid, []h248.ActionReply{{
		ContextID: contextID,
		Commands:  []string{"A=" + physicalTermination, media},
	}})
}

func isLineTermination(value, physicalTermination string) bool {
	return value == "*" || strings.EqualFold(value, physicalTermination)
}

func isCarrierClear(command h248.Command) bool {
	lower := strings.ToLower(command.Body)
	return strings.Contains(lower, "cg/ct") ||
		(strings.Contains(lower, "mo=rc") && strings.HasPrefix(strings.ToUpper(command.TerminationID), "RTP/"))
}
