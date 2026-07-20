package h248

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	eventRequestRe = regexp.MustCompile(`(?i)\b(?:Events|E)\s*=\s*([0-9]+)\s*\{`)
	andispDDBRe    = regexp.MustCompile(`(?is)\bandisp/dwa\s*\{[^}]*\bddb\s*=\s*"?([0-9a-f]+)`)
	connectionRe   = regexp.MustCompile(`(?im)^\s*c\s*=\s*IN\s+IP4\s+([^\s}\r\n]+)`)
	audioRe        = regexp.MustCompile(`(?im)^\s*m\s*=\s*audio\s+([0-9]+)\s+RTP/AVP\s+([^\r\n}]+)`)
	rtpmapRe       = regexp.MustCompile(`(?im)^\s*a\s*=\s*rtpmap\s*:\s*([0-9]+)\s+telephone-event/8000`)
)

type MediaDescription struct {
	IP                    string
	Port                  int
	PayloadTypes          []int
	TelephoneEventPayload int
}

func BuildNotify(version int, mid, transactionID, contextID, termination, requestID string, observedAt time.Time, event string) string {
	if version <= 0 {
		version = 1
	}
	if contextID == "" {
		contextID = "-"
	}
	return fmt.Sprintf("!/%d %s\nT=%s{C=%s{N=%s{OE=%s{%s:%s}}}}\n",
		version,
		FormatMID(mid),
		transactionID,
		contextID,
		termination,
		requestID,
		FormatTimestamp(observedAt),
		event,
	)
}

func BuildDigitMapCompleteNotify(version int, mid, transactionID, contextID, termination, requestID string, observedAt time.Time, number string) string {
	number = strings.ReplaceAll(number, `"`, "")
	event := fmt.Sprintf(`dd/ce{ds="%s",Meth=UM}`, number)
	return BuildNotify(version, mid, transactionID, contextID, termination, requestID, observedAt, event)
}

func FormatTimestamp(value time.Time) string {
	return value.Format("20060102T150405") + fmt.Sprintf("%02d", value.Nanosecond()/10_000_000)
}

func EventRequestID(raw string) string {
	match := eventRequestRe.FindStringSubmatch(raw)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func ContainsRingSignal(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.Contains(lower, "al/ri") ||
		strings.Contains(lower, "cg/rt") ||
		strings.Contains(lower, "andisp/dwa")
}

// DecodeAndispCallerID decodes the observed andisp/dwa DDB payload.
// The field format is two binary header octets followed by MMDDhhmm and the
// caller number as ASCII digits, terminated by '$'. Unknown payloads are left
// undecoded instead of guessing a number.
func DecodeAndispCallerID(raw string) string {
	match := andispDDBRe.FindStringSubmatch(raw)
	if len(match) != 2 || len(match[1])%2 != 0 {
		return ""
	}
	decoded, err := hex.DecodeString(match[1])
	if err != nil {
		return ""
	}
	var printable strings.Builder
	for _, value := range decoded {
		if value >= 0x20 && value <= 0x7e {
			printable.WriteByte(value)
		}
	}
	text := strings.Trim(printable.String(), " $\x00")
	if len(text) > 8 && allDigits(text) && validCallerIDTimestamp(text[:8]) {
		candidate := text[8:]
		if len(candidate) >= 3 && len(candidate) <= 20 {
			return candidate
		}
	}
	return ""
}

func ExtractRemoteMedia(raw string) (MediaDescription, bool) {
	for _, body := range descriptorBodies(raw, "R") {
		media, ok := parseMediaDescription(body)
		if ok {
			return media, true
		}
	}
	return MediaDescription{}, false
}

// ExtractLocalTelephoneEventPayload returns the RFC4733 payload type offered
// by the MGC in an H.248 LocalDescriptor.  During SIP-originated calls the MGC
// sends a wildcard local address/port, so the complete media parser cannot be
// used even though its payload mapping is authoritative for the MG's reply.
func ExtractLocalTelephoneEventPayload(raw string) (int, bool) {
	for _, body := range descriptorBodies(raw, "L") {
		body = normalizeDescriptorSDP(body)
		match := rtpmapRe.FindStringSubmatch(body)
		if len(match) != 2 {
			continue
		}
		payload, err := strconv.Atoi(match[1])
		if err == nil && payload > 0 && payload < 128 {
			return payload, true
		}
	}
	return 0, false
}

func parseMediaDescription(text string) (MediaDescription, bool) {
	text = normalizeDescriptorSDP(text)
	connections := connectionRe.FindAllStringSubmatch(text, -1)
	audio := audioRe.FindAllStringSubmatch(text, -1)
	if len(connections) == 0 || len(audio) == 0 {
		return MediaDescription{}, false
	}
	ip := connections[len(connections)-1][1]
	if ip == "" || ip == "$" {
		return MediaDescription{}, false
	}
	port, err := strconv.Atoi(audio[len(audio)-1][1])
	if err != nil || port <= 0 || port > 65535 {
		return MediaDescription{}, false
	}
	media := MediaDescription{IP: ip, Port: port}
	for _, field := range strings.Fields(audio[len(audio)-1][2]) {
		payload, err := strconv.Atoi(field)
		if err == nil && payload >= 0 && payload < 128 {
			media.PayloadTypes = append(media.PayloadTypes, payload)
		}
	}
	if match := rtpmapRe.FindStringSubmatch(text); len(match) == 2 {
		media.TelephoneEventPayload, _ = strconv.Atoi(match[1])
	}
	return media, true
}

func normalizeDescriptorSDP(text string) string {
	text = strings.ReplaceAll(text, `\r\n`, "\n")
	return strings.ReplaceAll(text, `\n`, "\n")
}

func descriptorBodies(raw, token string) []string {
	var bodies []string
	upper := strings.ToUpper(raw)
	token = strings.ToUpper(token)
	for i := 0; i < len(raw); {
		index := strings.Index(upper[i:], token)
		if index < 0 {
			break
		}
		index += i
		beforeOK := index == 0 || !isNameChar(raw[index-1])
		cursor := index + len(token)
		for cursor < len(raw) && isSpace(raw[cursor]) {
			cursor++
		}
		if beforeOK && cursor < len(raw) && raw[cursor] == '{' {
			if end := balancedEnd(raw, cursor); end >= 0 {
				bodies = append(bodies, raw[cursor+1:end])
				i = end + 1
				continue
			}
		}
		i = index + len(token)
	}
	return bodies
}

func allDigits(value string) bool {
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return value != ""
}

func validCallerIDTimestamp(value string) bool {
	if len(value) != 8 || !allDigits(value) {
		return false
	}
	month, _ := strconv.Atoi(value[0:2])
	day, _ := strconv.Atoi(value[2:4])
	hour, _ := strconv.Atoi(value[4:6])
	minute, _ := strconv.Atoi(value[6:8])
	return month >= 1 && month <= 12 && day >= 1 && day <= 31 && hour <= 23 && minute <= 59
}
