package h248

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

type Message struct {
	Raw                  string
	Version              int
	MID                  string
	TransactionType      string
	TransactionID        string
	ContextID            string
	ErrorCode            string
	ServiceChangeProfile string
	Commands             []Command
}

type Command struct {
	Name          string
	TerminationID string
	Body          string
	ContextID     string
	Optional      bool
}

var (
	headerRe         = regexp.MustCompile(`(?i)^(?:MEGACO|!)\s*/\s*([0-9]+)\s+([^\s\r\n]+)`)
	transactionRe    = regexp.MustCompile(`(?i)\b(Transaction|Reply|Pending|T|P|PN)\s*=\s*([0-9]+)`)
	contextRe        = regexp.MustCompile(`(?i)\bC\s*=\s*([^\{\s]+)`)
	longCommandRe    = regexp.MustCompile(`(?is)\b(ServiceChange|AuditCapabilities|AuditValue|Subtract|Modify|Notify|Move|Add)\s*=\s*([^\{\}\s,]+)?\s*(\{[^{}]*(?:\{[^{}]*\}[^{}]*)*\})?`)
	compactCommandRe = regexp.MustCompile(`(?s)\b(SC|AC|AV|MF|MV|A|S|N)\s*=\s*([^\{\}\s,]+)?\s*(\{[^{}]*(?:\{[^{}]*\}[^{}]*)*\})?`)
	errorRe          = regexp.MustCompile(`(?i)\b(?:Error|ER)\s*=\s*([0-9]+)`)
	profileRe        = regexp.MustCompile(`(?i)\b(?:Profile|PF)\s*=\s*"?([^,}\s"]+)`)
)

func Parse(data []byte) (*Message, error) {
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil, fmt.Errorf("empty H.248 message")
	}
	msg := &Message{Raw: raw}
	if m := headerRe.FindStringSubmatch(raw); len(m) == 3 {
		msg.Version, _ = strconv.Atoi(m[1])
		msg.MID = m[2]
	}
	if m := transactionRe.FindStringSubmatchIndex(raw); len(m) == 6 {
		msg.TransactionType = canonicalTransactionType(raw[m[2]:m[3]])
		msg.TransactionID = raw[m[4]:m[5]]
		if body, ok := assignmentBody(raw, m[1]); ok {
			parseActions(msg, body)
		}
	}
	if len(msg.Commands) == 0 {
		// Some diagnostic and legacy messages wrap a transaction in a
		// non-standard envelope. Keep a tolerant fallback for those messages;
		// normal wire traffic is parsed structurally above.
		if m := contextRe.FindStringSubmatch(raw); len(m) == 2 {
			msg.ContextID = strings.Trim(m[1], `"`)
		}
		for _, m := range append(longCommandRe.FindAllStringSubmatch(raw, -1), compactCommandRe.FindAllStringSubmatch(raw, -1)...) {
			cmd := Command{Name: canonicalCommand(m[1]), ContextID: msg.ContextID}
			if len(m) > 2 {
				cmd.TerminationID = strings.Trim(strings.TrimSpace(m[2]), `"`)
			}
			if len(m) > 3 {
				cmd.Body = strings.TrimSpace(m[3])
			}
			msg.Commands = append(msg.Commands, cmd)
		}
	}
	if m := errorRe.FindStringSubmatch(raw); len(m) == 2 {
		msg.ErrorCode = m[1]
	}
	if m := profileRe.FindStringSubmatch(raw); len(m) == 2 {
		msg.ServiceChangeProfile = m[1]
	}
	if len(msg.Commands) == 0 && msg.TransactionID == "" {
		return nil, fmt.Errorf("unsupported H.248 message")
	}
	return msg, nil
}

type assignment struct {
	name     string
	value    string
	body     string
	optional bool
}

func parseActions(msg *Message, transactionBody string) {
	for _, action := range parseAssignments(transactionBody) {
		if !strings.EqualFold(action.name, "C") || action.body == "" {
			continue
		}
		contextID := strings.Trim(strings.TrimSpace(action.value), `"`)
		if msg.ContextID == "" {
			msg.ContextID = contextID
		}
		for _, item := range parseAssignments(trimOuterBraces(action.body)) {
			name := canonicalCommand(item.name)
			if !isCommand(name) {
				continue
			}
			msg.Commands = append(msg.Commands, Command{
				Name:          name,
				TerminationID: strings.Trim(strings.TrimSpace(item.value), `"`),
				Body:          strings.TrimSpace(item.body),
				ContextID:     contextID,
				Optional:      item.optional,
			})
		}
	}
}

func parseAssignments(text string) []assignment {
	items := make([]assignment, 0, 4)
	for i := 0; i < len(text); {
		for i < len(text) && (isSpace(text[i]) || text[i] == ',') {
			i++
		}
		if i >= len(text) || text[i] == '}' {
			break
		}

		start := i
		for i < len(text) && (isNameChar(text[i]) || text[i] == '-') {
			i++
		}
		if start == i {
			i++
			continue
		}
		name := strings.TrimSpace(text[start:i])
		for i < len(text) && isSpace(text[i]) {
			i++
		}
		if i >= len(text) || text[i] != '=' {
			i = skipUnknownItem(text, i)
			continue
		}
		i++
		for i < len(text) && isSpace(text[i]) {
			i++
		}

		valueStart := i
		quoted := false
		if i < len(text) && text[i] == '"' {
			quoted = true
			i++
			for i < len(text) {
				if text[i] == '"' && (i == 0 || text[i-1] != '\\') {
					i++
					break
				}
				i++
			}
		} else {
			for i < len(text) && !isSpace(text[i]) && text[i] != '{' && text[i] != '}' && text[i] != ',' {
				i++
			}
		}
		value := strings.TrimSpace(text[valueStart:i])
		if quoted {
			value = strings.Trim(value, `"`)
		}
		for i < len(text) && isSpace(text[i]) {
			i++
		}

		body := ""
		if i < len(text) && text[i] == '{' {
			end := balancedEnd(text, i)
			if end < 0 {
				body = text[i:]
				i = len(text)
			} else {
				body = text[i : end+1]
				i = end + 1
			}
		}
		optional := strings.HasPrefix(strings.ToUpper(name), "O-")
		if optional {
			name = name[2:]
		}
		items = append(items, assignment{name: name, value: value, body: body, optional: optional})
	}
	return items
}

func assignmentBody(text string, after int) (string, bool) {
	for after < len(text) && isSpace(text[after]) {
		after++
	}
	if after >= len(text) || text[after] != '{' {
		return "", false
	}
	end := balancedEnd(text, after)
	if end < 0 {
		return "", false
	}
	return text[after+1 : end], true
}

func balancedEnd(text string, start int) int {
	depth := 0
	quoted := false
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '"':
			if i == 0 || text[i-1] != '\\' {
				quoted = !quoted
			}
		case '{':
			if !quoted {
				depth++
			}
		case '}':
			if !quoted {
				depth--
				if depth == 0 {
					return i
				}
			}
		}
	}
	return -1
}

func skipUnknownItem(text string, i int) int {
	for i < len(text) && text[i] != ',' && text[i] != '}' {
		if text[i] == '{' {
			if end := balancedEnd(text, i); end >= 0 {
				return end + 1
			}
			return len(text)
		}
		i++
	}
	return i
}

func trimOuterBraces(text string) string {
	text = strings.TrimSpace(text)
	if len(text) >= 2 && text[0] == '{' && text[len(text)-1] == '}' {
		return text[1 : len(text)-1]
	}
	return text
}

func isSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func isNameChar(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
}

func isCommand(name string) bool {
	switch name {
	case "ServiceChange", "Add", "Modify", "Subtract", "Move", "Notify", "AuditValue", "AuditCapabilities":
		return true
	default:
		return false
	}
}

func canonicalTransactionType(name string) string {
	switch strings.ToLower(name) {
	case "transaction", "t":
		return "Transaction"
	case "reply", "p":
		return "Reply"
	case "pending", "pn":
		return "Pending"
	default:
		return name
	}
}

func canonicalCommand(name string) string {
	switch strings.ToLower(name) {
	case "servicechange", "sc":
		return "ServiceChange"
	case "add", "a":
		return "Add"
	case "modify", "mf":
		return "Modify"
	case "subtract", "s":
		return "Subtract"
	case "move", "mv":
		return "Move"
	case "notify", "n":
		return "Notify"
	case "auditvalue", "av":
		return "AuditValue"
	case "auditcapabilities", "ac":
		return "AuditCapabilities"
	default:
		return name
	}
}

func BuildReply(req *Message, mgID string, commandReplies []string) string {
	tid := req.TransactionID
	if tid == "" {
		tid = "1"
	}
	if len(commandReplies) == 0 {
		commandReplies = []string{"Reply = 200"}
	}
	version := req.Version
	if version <= 0 {
		version = 1
	}
	return fmt.Sprintf("!/%d %s\nP=%s{\n  %s\n}\n",
		version,
		FormatMID(mgID),
		tid,
		strings.Join(commandReplies, "\n  "),
	)
}

// ActionReply is one H.248 action reply. Multiple command replies belonging to
// the same context must be emitted inside one C= block; several deployed MGCs
// retransmit a transaction when each command is wrapped in a separate C= block.
type ActionReply struct {
	ContextID string
	Commands  []string
}

func BuildActionReply(req *Message, mgID string, actions []ActionReply) string {
	parts := make([]string, 0, len(actions))
	for _, action := range actions {
		contextID := action.ContextID
		if contextID == "" {
			contextID = "-"
		}
		if len(action.Commands) == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("C=%s{%s}", contextID, strings.Join(action.Commands, ",")))
	}
	return BuildReply(req, mgID, parts)
}

func BuildErrorReply(req *Message, mgID, code, description string) string {
	description = strings.ReplaceAll(description, `"`, `'`)
	if description == "" {
		return BuildReply(req, mgID, []string{fmt.Sprintf("ER=%s", code)})
	}
	return BuildReply(req, mgID, []string{fmt.Sprintf("ER=%s{%q}", code, description)})
}

func CompactCommand(name string) string {
	switch name {
	case "ServiceChange":
		return "SC"
	case "Add":
		return "A"
	case "Modify":
		return "MF"
	case "Subtract":
		return "S"
	case "Move":
		return "MV"
	case "Notify":
		return "N"
	case "AuditValue":
		return "AV"
	case "AuditCapabilities":
		return "AC"
	default:
		return ""
	}
}

func SimpleCommandReply(command Command) string {
	token := CompactCommand(command.Name)
	if command.TerminationID == "" {
		return token
	}
	return token + "=" + command.TerminationID
}

type ServiceChangeOptions struct {
	Version       int
	MID           string
	RawMID        bool
	TransactionID string
	Termination   string
	Method        string
	Reason        string
	Profile       string
	Address       string
}

func BuildServiceChange(options ServiceChangeOptions) string {
	version := options.Version
	if version <= 0 {
		version = 1
	}
	method := "RS"
	if !strings.EqualFold(options.Method, "Restart") && options.Method != "" {
		method = options.Method
	}
	reason := strings.ReplaceAll(options.Reason, `"`, `'`)
	if reason == "" {
		reason = "901 Cold Boot"
	}
	descriptors := fmt.Sprintf("MT=%s,RE=\"%s\",V=%d", method, reason, version)
	if options.Address != "" {
		descriptors += ",AD=" + options.Address
	}
	if options.Profile != "" {
		descriptors += ",PF=" + options.Profile
	}
	mid := FormatMID(options.MID)
	if options.RawMID {
		mid = strings.TrimSpace(options.MID)
		if mid == "" {
			mid = "0"
		}
	}
	termination := strings.TrimSpace(options.Termination)
	if termination == "" {
		termination = "ROOT"
	}
	return fmt.Sprintf("!/%d %s\nT=%s{C=-{SC=%s{SV{%s}}}}\n",
		version,
		mid,
		options.TransactionID,
		termination,
		descriptors,
	)
}

func FormatMID(mid string) string {
	mid = strings.TrimSpace(mid)
	if mid == "" {
		return "0"
	}
	if strings.HasPrefix(mid, "[") || net.ParseIP(mid) == nil {
		return mid
	}
	return "[" + mid + "]"
}

func CommandOK(cmd Command, contextID string) string {
	if contextID == "" {
		contextID = "-"
	}
	if cmd.TerminationID == "" {
		return fmt.Sprintf("C=%s{%s=200}", contextID, cmd.Name)
	}
	return fmt.Sprintf("C=%s{%s=%s{}}", contextID, cmd.Name, cmd.TerminationID)
}
