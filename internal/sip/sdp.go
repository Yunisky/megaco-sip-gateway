package sip

import (
	"fmt"
	"strconv"
	"strings"
)

type SDP struct {
	IP                    string
	Port                  int
	Codecs                []string
	PayloadTypes          []int
	TelephoneEventPayload int
	PacketizationTime     int
	Direction             string
}

func BuildSDP(origin, ip string, port int, codecs []string, dtmfPayloadType int) string {
	return BuildSDPWithPTime(origin, ip, port, codecs, dtmfPayloadType, 20)
}

func BuildSDPWithPTime(origin, ip string, port int, codecs []string, dtmfPayloadType, ptime int) string {
	payloads := make([]string, 0, len(codecs)+1)
	rtpmap := make([]string, 0, len(codecs)+2)
	for _, codec := range codecs {
		switch strings.ToUpper(codec) {
		case "PCMA/8000":
			payloads = append(payloads, "8")
			rtpmap = append(rtpmap, "a=rtpmap:8 PCMA/8000")
		case "PCMU/8000":
			payloads = append(payloads, "0")
			rtpmap = append(rtpmap, "a=rtpmap:0 PCMU/8000")
		case "G729/8000", "G.729/8000":
			payloads = append(payloads, "18")
			rtpmap = append(rtpmap, "a=rtpmap:18 G729/8000")
		}
	}
	if len(payloads) == 0 {
		payloads = []string{"8", "0"}
		rtpmap = []string{"a=rtpmap:8 PCMA/8000", "a=rtpmap:0 PCMU/8000"}
	}
	if dtmfPayloadType > 0 && dtmfPayloadType < 128 {
		payload := fmt.Sprintf("%d", dtmfPayloadType)
		payloads = append(payloads, payload)
		rtpmap = append(rtpmap,
			fmt.Sprintf("a=rtpmap:%s telephone-event/8000", payload),
			fmt.Sprintf("a=fmtp:%s 0-16", payload),
		)
	}
	lines := []string{
		"v=0",
		fmt.Sprintf("o=%s 1 1 IN IP4 %s", origin, ip),
		"s=H248-SIP Gateway",
		fmt.Sprintf("c=IN IP4 %s", ip),
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP %s", port, strings.Join(payloads, " ")),
	}
	lines = append(lines, rtpmap...)
	if ptime > 0 {
		lines = append(lines, fmt.Sprintf("a=ptime:%d", ptime))
	}
	lines = append(lines, "a=sendrecv")
	return strings.Join(lines, "\r\n") + "\r\n"
}

func ParseSDP(body string) (SDP, error) {
	text := strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	result := SDP{Direction: "sendrecv"}
	sessionIP := ""
	inAudio := false
	rtpmap := make(map[int]string)

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "m=") {
			inAudio = false
			fields := strings.Fields(strings.TrimPrefix(line, "m="))
			if len(fields) < 4 || !strings.EqualFold(fields[0], "audio") {
				continue
			}
			portText := strings.SplitN(fields[1], "/", 2)[0]
			port, err := strconv.Atoi(portText)
			if err != nil || port < 0 || port > 65535 {
				return SDP{}, fmt.Errorf("invalid SDP audio port %q", fields[1])
			}
			result.Port = port
			for _, value := range fields[3:] {
				payload, err := strconv.Atoi(value)
				if err == nil && payload >= 0 && payload < 128 {
					result.PayloadTypes = append(result.PayloadTypes, payload)
				}
			}
			inAudio = true
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "c=") {
			fields := strings.Fields(strings.TrimSpace(line[2:]))
			if len(fields) >= 3 && strings.EqualFold(fields[0], "IN") {
				if inAudio {
					result.IP = fields[2]
				} else {
					sessionIP = fields[2]
				}
			}
			continue
		}
		if !inAudio || !strings.HasPrefix(strings.ToLower(line), "a=") {
			continue
		}
		attribute := strings.TrimSpace(line[2:])
		lower := strings.ToLower(attribute)
		switch lower {
		case "sendrecv", "sendonly", "recvonly", "inactive":
			result.Direction = lower
			continue
		}
		if strings.HasPrefix(lower, "ptime:") {
			result.PacketizationTime, _ = strconv.Atoi(strings.TrimSpace(attribute[len("ptime:"):]))
			continue
		}
		if strings.HasPrefix(lower, "rtpmap:") {
			mapping := strings.Fields(strings.TrimSpace(attribute[len("rtpmap:"):]))
			if len(mapping) != 2 {
				continue
			}
			payload, err := strconv.Atoi(mapping[0])
			if err != nil {
				continue
			}
			rtpmap[payload] = mapping[1]
			if strings.HasPrefix(strings.ToLower(mapping[1]), "telephone-event/8000") {
				result.TelephoneEventPayload = payload
			}
		}
	}

	if result.IP == "" {
		result.IP = sessionIP
	}
	if result.IP == "" || result.Port == 0 {
		return SDP{}, fmt.Errorf("SDP has no active audio address")
	}
	for _, payload := range result.PayloadTypes {
		codec := strings.ToUpper(rtpmap[payload])
		switch {
		case payload == 8 || strings.HasPrefix(codec, "PCMA/8000"):
			result.Codecs = appendUnique(result.Codecs, "PCMA/8000")
		case payload == 0 || strings.HasPrefix(codec, "PCMU/8000"):
			result.Codecs = appendUnique(result.Codecs, "PCMU/8000")
		case payload == 18 || strings.HasPrefix(codec, "G729/8000"):
			result.Codecs = appendUnique(result.Codecs, "G729/8000")
		}
	}
	return result, nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
