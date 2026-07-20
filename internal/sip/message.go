package sip

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

type Message struct {
	StartLine string
	Method    string
	URI       string
	Status    int
	Reason    string
	Headers   map[string][]string
	Body      string
	Source    *net.UDPAddr
}

func Parse(data []byte) (*Message, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	head, body, _ := strings.Cut(text, "\n\n")
	lines := strings.Split(head, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nil, fmt.Errorf("empty SIP message")
	}

	msg := &Message{
		StartLine: strings.TrimSpace(lines[0]),
		Headers:   make(map[string][]string),
		Body:      body,
	}

	if strings.HasPrefix(msg.StartLine, "SIP/2.0 ") {
		fields := strings.Fields(msg.StartLine)
		if len(fields) < 2 {
			return nil, fmt.Errorf("invalid SIP response line %q", msg.StartLine)
		}
		status, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, err
		}
		msg.Status = status
		if len(fields) > 2 {
			msg.Reason = strings.Join(fields[2:], " ")
		}
	} else {
		fields := strings.Fields(msg.StartLine)
		if len(fields) != 3 || fields[2] != "SIP/2.0" {
			return nil, fmt.Errorf("invalid SIP request line %q", msg.StartLine)
		}
		msg.Method = fields[0]
		msg.URI = fields[1]
	}

	var current string
	for _, raw := range lines[1:] {
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t") {
			if current == "" {
				return nil, fmt.Errorf("folded SIP header without previous header")
			}
			values := msg.Headers[current]
			values[len(values)-1] += " " + strings.TrimSpace(raw)
			msg.Headers[current] = values
			continue
		}
		name, value, ok := strings.Cut(raw, ":")
		if !ok {
			return nil, fmt.Errorf("invalid SIP header %q", raw)
		}
		current = canonicalHeader(name)
		msg.Headers[current] = append(msg.Headers[current], strings.TrimSpace(value))
	}

	return msg, nil
}

func (m *Message) Header(name string) string {
	values := m.Headers[canonicalHeader(name)]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (m *Message) HeaderValues(name string) []string {
	values := m.Headers[canonicalHeader(name)]
	return append([]string(nil), values...)
}

func (m *Message) CallID() string {
	if v := m.Header("Call-ID"); v != "" {
		return v
	}
	return m.Header("i")
}

func (m *Message) CSeqMethod() string {
	fields := strings.Fields(m.Header("CSeq"))
	if len(fields) == 2 {
		return fields[1]
	}
	return ""
}

func (m *Message) CSeqNumber() string {
	fields := strings.Fields(m.Header("CSeq"))
	if len(fields) >= 1 {
		return fields[0]
	}
	return "1"
}

func canonicalHeader(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "v":
		return "Via"
	case "f":
		return "From"
	case "t":
		return "To"
	case "i":
		return "Call-ID"
	case "m":
		return "Contact"
	case "l":
		return "Content-Length"
	case "c":
		return "Content-Type"
	default:
		parts := strings.Split(strings.ToLower(strings.TrimSpace(name)), "-")
		for i := range parts {
			if parts[i] != "" {
				parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
			}
		}
		return strings.Join(parts, "-")
	}
}

func BuildResponse(req *Message, status int, reason, body, localContact string, extra map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SIP/2.0 %d %s\r\n", status, reason)
	for _, via := range req.HeaderValues("Via") {
		fmt.Fprintf(&b, "Via: %s\r\n", via)
	}
	fmt.Fprintf(&b, "From: %s\r\n", req.Header("From"))
	to := req.Header("To")
	if status != 100 && !strings.Contains(strings.ToLower(to), "tag=") {
		to += ";tag=gw"
	}
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Call-ID: %s\r\n", req.CallID())
	fmt.Fprintf(&b, "CSeq: %s\r\n", req.Header("CSeq"))
	if localContact != "" {
		fmt.Fprintf(&b, "Contact: <%s>\r\n", localContact)
	}
	for k, v := range extra {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	if body != "" {
		fmt.Fprintf(&b, "Content-Type: application/sdp\r\n")
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return b.String()
}

func BuildRequest(method, uri string, headers map[string]string, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s SIP/2.0\r\n", method, uri)
	for k, v := range headers {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	if body != "" {
		fmt.Fprintf(&b, "Content-Type: application/sdp\r\n")
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return b.String()
}
