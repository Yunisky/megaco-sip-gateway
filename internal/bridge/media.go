package bridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/Yunisky/megaco-sip-gateway/internal/socketbind"
)

type mediaRelay struct {
	logger        *slog.Logger
	callID        string
	h248Conn      *net.UDPConn
	sipConn       *net.UDPConn
	h248RTCPConn  *net.UDPConn
	sipRTCPConn   *net.UDPConn
	cancel        context.CancelFunc
	closeOnce     sync.Once
	mu            sync.RWMutex
	h248Peer      *net.UDPAddr
	sipPeer       *net.UDPAddr
	h248RTCPPeer  *net.UDPAddr
	sipRTCPPeer   *net.UDPAddr
	h248DTMF      int
	sipDTMF       int
	h248DTMFMode  string
	dtmfSamples   int
	h248ToSIP     atomic.Uint64
	sipToH248     atomic.Uint64
	h248RTCPToSIP atomic.Uint64
	sipRTCPToH248 atomic.Uint64
}

func newMediaRelay(
	ctx context.Context,
	logger *slog.Logger,
	callID string,
	h248IP string,
	h248Port int,
	h248Device string,
	sipIP string,
	sipPort int,
	sipDevice string,
	h248DTMF int,
	sipDTMF int,
	h248DTMFMode string,
	packetizationTimeMillis int,
) (*mediaRelay, error) {
	mediaCtx, cancel := context.WithCancel(ctx)
	h248Address := net.JoinHostPort(h248IP, strconv.Itoa(h248Port))
	h248Conn, err := socketbind.ListenUDP(mediaCtx, h248Address, h248Device)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("listen H.248 RTP %s: %w", h248Address, err)
	}
	h248RTCPAddress := net.JoinHostPort(h248IP, strconv.Itoa(h248Port+1))
	h248RTCPConn, err := socketbind.ListenUDP(mediaCtx, h248RTCPAddress, h248Device)
	if err != nil {
		_ = h248Conn.Close()
		cancel()
		return nil, fmt.Errorf("listen H.248 RTCP %s: %w", h248RTCPAddress, err)
	}
	sipAddress := net.JoinHostPort(sipIP, strconv.Itoa(sipPort))
	sipConn, err := socketbind.ListenUDP(mediaCtx, sipAddress, sipDevice)
	if err != nil {
		_ = h248Conn.Close()
		_ = h248RTCPConn.Close()
		cancel()
		return nil, fmt.Errorf("listen SIP RTP %s: %w", sipAddress, err)
	}
	sipRTCPAddress := net.JoinHostPort(sipIP, strconv.Itoa(sipPort+1))
	sipRTCPConn, err := socketbind.ListenUDP(mediaCtx, sipRTCPAddress, sipDevice)
	if err != nil {
		_ = h248Conn.Close()
		_ = h248RTCPConn.Close()
		_ = sipConn.Close()
		cancel()
		return nil, fmt.Errorf("listen SIP RTCP %s: %w", sipRTCPAddress, err)
	}
	relay := &mediaRelay{
		logger:       logger,
		callID:       callID,
		h248Conn:     h248Conn,
		sipConn:      sipConn,
		h248RTCPConn: h248RTCPConn,
		sipRTCPConn:  sipRTCPConn,
		cancel:       cancel,
		h248DTMF:     h248DTMF,
		sipDTMF:      sipDTMF,
		h248DTMFMode: h248DTMFMode,
		dtmfSamples:  packetizationTimeMillis * 8,
	}
	if relay.dtmfSamples <= 0 {
		relay.dtmfSamples = 160
	}
	go relay.forward(mediaCtx, h248Conn, "h248", sipConn, "sip")
	go relay.forward(mediaCtx, sipConn, "sip", h248Conn, "h248")
	go relay.forwardRTCP(mediaCtx, h248RTCPConn, "h248", sipRTCPConn, "sip")
	go relay.forwardRTCP(mediaCtx, sipRTCPConn, "sip", h248RTCPConn, "h248")
	return relay, nil
}

func (r *mediaRelay) SetH248Peer(peer *net.UDPAddr, telephoneEventPayload int) {
	r.mu.Lock()
	r.h248Peer = cloneUDPAddr(peer)
	r.h248RTCPPeer = adjacentRTCPAddr(peer)
	if telephoneEventPayload > 0 && telephoneEventPayload < 128 {
		r.h248DTMF = telephoneEventPayload
	}
	r.mu.Unlock()
}

func (r *mediaRelay) SetSIPPeer(peer *net.UDPAddr, telephoneEventPayload int) {
	r.mu.Lock()
	r.sipPeer = cloneUDPAddr(peer)
	r.sipRTCPPeer = adjacentRTCPAddr(peer)
	if telephoneEventPayload > 0 && telephoneEventPayload < 128 {
		r.sipDTMF = telephoneEventPayload
	}
	r.mu.Unlock()
}

func (r *mediaRelay) SetH248DTMFPayloadType(telephoneEventPayload int) {
	if telephoneEventPayload <= 0 || telephoneEventPayload >= 128 {
		return
	}
	r.mu.Lock()
	r.h248DTMF = telephoneEventPayload
	r.mu.Unlock()
}

func (r *mediaRelay) forwardRTCP(ctx context.Context, source *net.UDPConn, sourceSide string, destination *net.UDPConn, destinationSide string) {
	buffer := make([]byte, 65535)
	for {
		n, sourcePeer, err := source.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				r.logger.Warn("RTCP read failed", "call", r.callID, "side", sourceSide, "error", err)
			}
			return
		}
		peer := r.rtcpForwardingState(sourceSide, sourcePeer)
		if peer == nil {
			continue
		}
		if _, err := destination.WriteToUDP(buffer[:n], peer); err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				r.logger.Warn("RTCP write failed", "call", r.callID, "from", sourceSide, "to", destinationSide, "peer", peer, "error", err)
			}
			continue
		}
		if sourceSide == "h248" {
			r.h248RTCPToSIP.Add(1)
		} else {
			r.sipRTCPToH248.Add(1)
		}
	}
}

func (r *mediaRelay) forward(ctx context.Context, source *net.UDPConn, sourceSide string, destination *net.UDPConn, destinationSide string) {
	buffer := make([]byte, 65535)
	toneState := dtmfToneState{}
	for {
		n, sourcePeer, err := source.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				r.logger.Warn("RTP read failed", "call", r.callID, "side", sourceSide, "error", err)
			}
			return
		}
		packet := append([]byte(nil), buffer[:n]...)
		peer, sourceDTMF, destinationDTMF := r.forwardingState(sourceSide, sourcePeer)
		if peer == nil {
			continue
		}
		if sourceSide == "sip" && r.h248DTMFMode == "inband" && rtpPayloadType(packet) == sourceDTMF {
			var ok bool
			packet, ok = synthesizeTelephoneEventAsPCMA(packet, &toneState, r.dtmfSamples)
			if !ok {
				continue
			}
		} else {
			rewriteRTPPayloadType(packet, sourceDTMF, destinationDTMF)
		}
		if _, err := destination.WriteToUDP(packet, peer); err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				r.logger.Warn("RTP write failed", "call", r.callID, "from", sourceSide, "to", destinationSide, "peer", peer, "error", err)
			}
			continue
		}
		if sourceSide == "h248" {
			r.h248ToSIP.Add(1)
		} else {
			r.sipToH248.Add(1)
		}
	}
}

func (r *mediaRelay) rtcpForwardingState(sourceSide string, observedSource *net.UDPAddr) *net.UDPAddr {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sourceSide == "h248" {
		if observedSource != nil {
			r.h248RTCPPeer = cloneUDPAddr(observedSource)
		}
		return cloneUDPAddr(r.sipRTCPPeer)
	}
	if observedSource != nil {
		r.sipRTCPPeer = cloneUDPAddr(observedSource)
	}
	return cloneUDPAddr(r.h248RTCPPeer)
}

func (r *mediaRelay) forwardingState(sourceSide string, observedSource *net.UDPAddr) (*net.UDPAddr, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sourceSide == "h248" {
		// The carrier may use symmetric RTP even when the SDP port changes after
		// answer. Learn only the IP/port of media received on the carrier socket.
		if observedSource != nil {
			r.h248Peer = cloneUDPAddr(observedSource)
		}
		return cloneUDPAddr(r.sipPeer), r.h248DTMF, r.sipDTMF
	}
	if observedSource != nil {
		r.sipPeer = cloneUDPAddr(observedSource)
	}
	return cloneUDPAddr(r.h248Peer), r.sipDTMF, r.h248DTMF
}

func (r *mediaRelay) Close() {
	r.closeOnce.Do(func() {
		r.cancel()
		_ = r.h248Conn.Close()
		_ = r.sipConn.Close()
		_ = r.h248RTCPConn.Close()
		_ = r.sipRTCPConn.Close()
		r.logger.Info("RTP/RTCP relay stopped",
			"call", r.callID,
			"h248_to_sip_packets", r.h248ToSIP.Load(),
			"sip_to_h248_packets", r.sipToH248.Load(),
			"h248_to_sip_rtcp_packets", r.h248RTCPToSIP.Load(),
			"sip_to_h248_rtcp_packets", r.sipRTCPToH248.Load(),
		)
	})
}

func adjacentRTCPAddr(rtp *net.UDPAddr) *net.UDPAddr {
	if rtp == nil || rtp.Port <= 0 || rtp.Port >= 65535 {
		return nil
	}
	rtcp := cloneUDPAddr(rtp)
	rtcp.Port++
	return rtcp
}

func rewriteRTPPayloadType(packet []byte, sourcePayload, destinationPayload int) {
	if len(packet) < 12 || packet[0]>>6 != 2 || sourcePayload <= 0 || sourcePayload >= 128 || destinationPayload <= 0 || destinationPayload >= 128 {
		return
	}
	if int(packet[1]&0x7f) != sourcePayload {
		return
	}
	packet[1] = packet[1]&0x80 | byte(destinationPayload)
}

type dtmfToneState struct {
	active           bool
	event            byte
	ssrc             uint32
	eventTimestamp   uint32
	samplesGenerated uint32
}

func synthesizeTelephoneEventAsPCMA(packet []byte, state *dtmfToneState, samplesPerPacket int) ([]byte, bool) {
	headerLength, ok := rtpHeaderLength(packet)
	if !ok || len(packet) < headerLength+4 || samplesPerPacket <= 0 {
		return nil, false
	}
	event := packet[headerLength]
	low, high, ok := dtmfFrequencies(event)
	if !ok {
		return nil, false
	}
	ssrc := binary.BigEndian.Uint32(packet[8:12])
	eventTimestamp := binary.BigEndian.Uint32(packet[4:8])
	marker := packet[1]&0x80 != 0
	if !state.active || marker || state.event != event || state.ssrc != ssrc || state.eventTimestamp != eventTimestamp {
		*state = dtmfToneState{active: true, event: event, ssrc: ssrc, eventTimestamp: eventTimestamp}
	}

	out := make([]byte, headerLength+samplesPerPacket)
	copy(out, packet[:headerLength])
	out[0] &^= 0x20 // synthesized packets do not contain RTP padding
	out[1] = out[1]&0x80 | 8
	binary.BigEndian.PutUint32(out[4:8], state.eventTimestamp+state.samplesGenerated)
	for index := 0; index < samplesPerPacket; index++ {
		sampleIndex := float64(state.samplesGenerated + uint32(index))
		sample := 5500*math.Sin(2*math.Pi*low*sampleIndex/8000) +
			5500*math.Sin(2*math.Pi*high*sampleIndex/8000)
		out[headerLength+index] = linearToALaw(int16(math.Round(sample)))
	}
	state.samplesGenerated += uint32(samplesPerPacket)
	return out, true
}

func rtpPayloadType(packet []byte) int {
	if len(packet) < 12 || packet[0]>>6 != 2 {
		return -1
	}
	return int(packet[1] & 0x7f)
}

func rtpHeaderLength(packet []byte) (int, bool) {
	if len(packet) < 12 || packet[0]>>6 != 2 {
		return 0, false
	}
	headerLength := 12 + int(packet[0]&0x0f)*4
	if len(packet) < headerLength {
		return 0, false
	}
	if packet[0]&0x10 != 0 {
		if len(packet) < headerLength+4 {
			return 0, false
		}
		extensionWords := int(binary.BigEndian.Uint16(packet[headerLength+2 : headerLength+4]))
		headerLength += 4 + extensionWords*4
		if len(packet) < headerLength {
			return 0, false
		}
	}
	return headerLength, true
}

func dtmfFrequencies(event byte) (float64, float64, bool) {
	frequencies := [16][2]float64{
		{941, 1336}, {697, 1209}, {697, 1336}, {697, 1477},
		{770, 1209}, {770, 1336}, {770, 1477}, {852, 1209},
		{852, 1336}, {852, 1477}, {941, 1209}, {941, 1477},
		{697, 1633}, {770, 1633}, {852, 1633}, {941, 1633},
	}
	if int(event) >= len(frequencies) {
		return 0, 0, false
	}
	return frequencies[event][0], frequencies[event][1], true
}

func linearToALaw(sample int16) byte {
	value := int(sample)
	mask := byte(0xd5)
	if value < 0 {
		mask = 0x55
		value = -value - 8
	}
	if value < 0 {
		value = 0
	}
	if value > 32635 {
		value = 32635
	}
	segment := 0
	for threshold := 64; segment < 7 && value >= threshold; threshold <<= 1 {
		segment++
	}
	encoded := byte(segment << 4)
	if segment < 2 {
		encoded |= byte(value>>4) & 0x0f
	} else {
		encoded |= byte(value>>(segment+3)) & 0x0f
	}
	return encoded ^ mask
}
