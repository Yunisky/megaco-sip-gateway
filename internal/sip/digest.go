package sip

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

type DigestChallenge struct {
	Realm     string
	Nonce     string
	Opaque    string
	Algorithm string
	QOP       []string
	Stale     bool
}

type DigestAuthorizationOptions struct {
	Method     string
	URI        string
	Username   string
	Password   string
	Body       string
	NonceCount uint32
	CNonce     string
}

func ParseDigestChallenge(value string) (DigestChallenge, error) {
	scheme, parameters, ok := strings.Cut(strings.TrimSpace(value), " ")
	if !ok || !strings.EqualFold(scheme, "Digest") {
		return DigestChallenge{}, fmt.Errorf("unsupported authentication challenge %q", value)
	}
	values := make(map[string]string)
	for _, item := range splitDigestParameters(parameters) {
		name, parameter, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		values[strings.ToLower(strings.TrimSpace(name))] = unquoteDigestValue(parameter)
	}
	challenge := DigestChallenge{
		Realm:     values["realm"],
		Nonce:     values["nonce"],
		Opaque:    values["opaque"],
		Algorithm: values["algorithm"],
		Stale:     strings.EqualFold(values["stale"], "true"),
	}
	if challenge.Realm == "" || challenge.Nonce == "" {
		return DigestChallenge{}, fmt.Errorf("digest challenge is missing realm or nonce")
	}
	if challenge.Algorithm == "" {
		challenge.Algorithm = "MD5"
	}
	for _, qop := range strings.Split(values["qop"], ",") {
		if qop = strings.ToLower(strings.TrimSpace(qop)); qop != "" {
			challenge.QOP = append(challenge.QOP, qop)
		}
	}
	return challenge, nil
}

func BuildDigestAuthorization(challenge DigestChallenge, options DigestAuthorizationOptions) (string, error) {
	if options.Method == "" || options.URI == "" || options.Username == "" {
		return "", fmt.Errorf("digest method, URI, and username are required")
	}
	algorithm := strings.ToUpper(strings.TrimSpace(challenge.Algorithm))
	sessionAlgorithm := strings.HasSuffix(algorithm, "-SESS")
	baseAlgorithm := strings.TrimSuffix(algorithm, "-SESS")
	hash, err := digestHash(baseAlgorithm)
	if err != nil {
		return "", err
	}
	qop := selectDigestQOP(challenge.QOP)
	if len(challenge.QOP) > 0 && qop == "" {
		return "", fmt.Errorf("digest qop values %q do not include auth or auth-int", challenge.QOP)
	}
	if (qop != "" || sessionAlgorithm) && options.CNonce == "" {
		return "", fmt.Errorf("digest cnonce is required for qop or session algorithms")
	}

	ha1 := hash(options.Username + ":" + challenge.Realm + ":" + options.Password)
	if sessionAlgorithm {
		ha1 = hash(ha1 + ":" + challenge.Nonce + ":" + options.CNonce)
	}
	ha2Input := options.Method + ":" + options.URI
	if qop == "auth-int" {
		ha2Input += ":" + hash(options.Body)
	}
	ha2 := hash(ha2Input)
	nonceCount := fmt.Sprintf("%08x", options.NonceCount)
	responseInput := ha1 + ":" + challenge.Nonce + ":"
	if qop == "" {
		responseInput += ha2
	} else {
		responseInput += nonceCount + ":" + options.CNonce + ":" + qop + ":" + ha2
	}

	parameters := []string{
		`username="` + escapeDigestValue(options.Username) + `"`,
		`realm="` + escapeDigestValue(challenge.Realm) + `"`,
		`nonce="` + escapeDigestValue(challenge.Nonce) + `"`,
		`uri="` + escapeDigestValue(options.URI) + `"`,
		`response="` + hash(responseInput) + `"`,
		"algorithm=" + challenge.Algorithm,
	}
	if challenge.Opaque != "" {
		parameters = append(parameters, `opaque="`+escapeDigestValue(challenge.Opaque)+`"`)
	}
	if qop != "" {
		parameters = append(parameters,
			"qop="+qop,
			"nc="+nonceCount,
			`cnonce="`+escapeDigestValue(options.CNonce)+`"`,
		)
	}
	return "Digest " + strings.Join(parameters, ", "), nil
}

func splitDigestParameters(value string) []string {
	var parameters []string
	start := 0
	quoted := false
	escaped := false
	for index, character := range value {
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' && quoted {
			escaped = true
			continue
		}
		if character == '"' {
			quoted = !quoted
			continue
		}
		if character == ',' && !quoted {
			parameters = append(parameters, strings.TrimSpace(value[start:index]))
			start = index + 1
		}
	}
	parameters = append(parameters, strings.TrimSpace(value[start:]))
	return parameters
}

func unquoteDigestValue(value string) string {
	value = strings.TrimSpace(value)
	if unquoted, err := strconv.Unquote(value); err == nil {
		return unquoted
	}
	return strings.Trim(value, `"`)
}

func escapeDigestValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

func selectDigestQOP(values []string) string {
	for _, value := range values {
		if strings.EqualFold(value, "auth") {
			return "auth"
		}
	}
	for _, value := range values {
		if strings.EqualFold(value, "auth-int") {
			return "auth-int"
		}
	}
	return ""
}

func digestHash(algorithm string) (func(string) string, error) {
	switch strings.ToUpper(algorithm) {
	case "MD5":
		return func(value string) string {
			sum := md5.Sum([]byte(value))
			return hex.EncodeToString(sum[:])
		}, nil
	case "SHA-256":
		return func(value string) string {
			sum := sha256.Sum256([]byte(value))
			return hex.EncodeToString(sum[:])
		}, nil
	default:
		return nil, fmt.Errorf("unsupported digest algorithm %q", algorithm)
	}
}
