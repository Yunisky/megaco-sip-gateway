package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Yunisky/megaco-sip-gateway/internal/sip"
	"github.com/Yunisky/megaco-sip-gateway/internal/socketbind"
)

type options struct {
	Target       string
	Source       string
	AdvertisedIP string
	BindDevice   string
	Username     string
	PasswordFile string
	Domain       string
	Expires      int
	Hold         time.Duration
	Timeout      time.Duration
	Unregister   bool
}

func main() {
	var cfg options
	flag.StringVar(&cfg.Target, "target", "127.0.0.1:5060", "SIP registrar UDP address")
	flag.StringVar(&cfg.Source, "source", "0.0.0.0:0", "local SIP UDP address")
	flag.StringVar(&cfg.AdvertisedIP, "advertised-ip", "", "IP placed in Via and Contact; auto-detected when empty")
	flag.StringVar(&cfg.BindDevice, "bind-device", "", "Linux interface or VRF device")
	flag.StringVar(&cfg.Username, "username", "", "SIP authentication username")
	flag.StringVar(&cfg.PasswordFile, "password-file", "", "file containing only the SIP password")
	flag.StringVar(&cfg.Domain, "domain", "", "SIP registration domain; target host when empty")
	flag.IntVar(&cfg.Expires, "expires", 300, "registration lifetime in seconds")
	flag.DurationVar(&cfg.Hold, "hold", 2*time.Second, "time to keep the registered socket open and answer OPTIONS")
	flag.DurationVar(&cfg.Timeout, "timeout", 30*time.Second, "overall timeout")
	flag.BoolVar(&cfg.Unregister, "unregister", true, "send an authenticated Expires: 0 before exit")
	flag.Parse()
	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

func run(cfg options) error {
	if cfg.Username == "" || cfg.PasswordFile == "" {
		return fmt.Errorf("username and password-file are required")
	}
	passwordBytes, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		return fmt.Errorf("read password file: %w", err)
	}
	password := strings.TrimSpace(string(passwordBytes))
	if password == "" {
		return fmt.Errorf("password file is empty")
	}
	remote, err := net.ResolveUDPAddr("udp", cfg.Target)
	if err != nil {
		return err
	}
	if cfg.AdvertisedIP == "" {
		cfg.AdvertisedIP, err = routeSourceIP(remote)
		if err != nil {
			return err
		}
	}
	if cfg.Domain == "" {
		cfg.Domain = strings.Trim(remote.IP.String(), "[]")
	}
	source, err := net.ResolveUDPAddr("udp", cfg.Source)
	if err != nil {
		return err
	}
	if source.IP == nil || source.IP.IsUnspecified() {
		source.IP = net.ParseIP(cfg.AdvertisedIP)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	connection, err := socketbind.ListenUDP(ctx, source.String(), cfg.BindDevice)
	if err != nil {
		return err
	}
	defer connection.Close()
	local := connection.LocalAddr().(*net.UDPAddr)
	contact := fmt.Sprintf("<sip:%s@%s;transport=udp>", cfg.Username, net.JoinHostPort(cfg.AdvertisedIP, strconv.Itoa(local.Port)))
	requestURI := "sip:" + cfg.Domain
	identity := fmt.Sprintf("<sip:%s@%s>", cfg.Username, cfg.Domain)
	callID := fmt.Sprintf("register-%d@%s", time.Now().UnixNano(), cfg.AdvertisedIP)
	from := identity + ";tag=" + randomToken(8)

	first := buildRegister(cfg, requestURI, from, identity, contact, callID, 1, cfg.Expires, "")
	challengeResponse, err := transact(ctx, connection, remote, first, callID, "REGISTER")
	if err != nil {
		return err
	}
	if challengeResponse.Status != 401 {
		return fmt.Errorf("initial REGISTER returned %d %s, want 401", challengeResponse.Status, challengeResponse.Reason)
	}
	challenge, err := sip.ParseDigestChallenge(challengeResponse.Header("WWW-Authenticate"))
	if err != nil {
		return err
	}
	cnonce := randomToken(12)
	authorization, err := sip.BuildDigestAuthorization(challenge, sip.DigestAuthorizationOptions{
		Method:     "REGISTER",
		URI:        requestURI,
		Username:   cfg.Username,
		Password:   password,
		NonceCount: 1,
		CNonce:     cnonce,
	})
	if err != nil {
		return err
	}
	second := buildRegister(cfg, requestURI, from, identity, contact, callID, 2, cfg.Expires, authorization)
	registered, err := transact(ctx, connection, remote, second, callID, "REGISTER")
	if err != nil {
		return err
	}
	if registered.Status != 200 {
		return fmt.Errorf("authenticated REGISTER returned %d %s", registered.Status, registered.Reason)
	}
	fmt.Printf("SIP registration successful: user=%s registrar=%s contact=%s expires=%d algorithm=%s\n", cfg.Username, remote, contact, cfg.Expires, challenge.Algorithm)

	optionsAnswered := 0
	holdUntil := time.Now().Add(cfg.Hold)
	for time.Now().Before(holdUntil) {
		deadline := holdUntil
		if deadline.After(time.Now().Add(time.Second)) {
			deadline = time.Now().Add(time.Second)
		}
		_ = connection.SetReadDeadline(deadline)
		buffer := make([]byte, 65535)
		count, peer, readErr := connection.ReadFromUDP(buffer)
		if readErr != nil {
			if networkError, ok := readErr.(net.Error); ok && networkError.Timeout() {
				continue
			}
			return readErr
		}
		message, parseErr := sip.Parse(buffer[:count])
		if parseErr != nil || message.Status != 0 {
			continue
		}
		if message.Method == "OPTIONS" {
			response := sip.BuildResponse(message, 200, "OK", "", "", map[string]string{"Allow": "INVITE, ACK, CANCEL, BYE, OPTIONS"})
			if _, writeErr := connection.WriteToUDP([]byte(response), peer); writeErr != nil {
				return writeErr
			}
			optionsAnswered++
		}
	}
	fmt.Printf("registration hold complete: OPTIONS answered=%d\n", optionsAnswered)
	if !cfg.Unregister {
		return nil
	}
	authorization, err = sip.BuildDigestAuthorization(challenge, sip.DigestAuthorizationOptions{
		Method:     "REGISTER",
		URI:        requestURI,
		Username:   cfg.Username,
		Password:   password,
		NonceCount: 2,
		CNonce:     cnonce,
	})
	if err != nil {
		return err
	}
	unregister := buildRegister(cfg, requestURI, from, identity, contact, callID, 3, 0, authorization)
	unregistered, err := transact(ctx, connection, remote, unregister, callID, "REGISTER")
	if err != nil {
		return err
	}
	if unregistered.Status == 401 {
		challenge, err = sip.ParseDigestChallenge(unregistered.Header("WWW-Authenticate"))
		if err != nil {
			return fmt.Errorf("parse unregister challenge: %w", err)
		}
		cnonce = randomToken(12)
		authorization, err = sip.BuildDigestAuthorization(challenge, sip.DigestAuthorizationOptions{
			Method:     "REGISTER",
			URI:        requestURI,
			Username:   cfg.Username,
			Password:   password,
			NonceCount: 1,
			CNonce:     cnonce,
		})
		if err != nil {
			return err
		}
		unregister = buildRegister(cfg, requestURI, from, identity, contact, callID, 4, 0, authorization)
		unregistered, err = transact(ctx, connection, remote, unregister, callID, "REGISTER")
		if err != nil {
			return err
		}
	}
	if unregistered.Status != 200 {
		return fmt.Errorf("unregister returned %d %s", unregistered.Status, unregistered.Reason)
	}
	fmt.Printf("SIP unregistration successful: user=%s\n", cfg.Username)
	return nil
}

func buildRegister(cfg options, requestURI, from, to, contact, callID string, cseq, expires int, authorization string) string {
	headers := map[string]string{
		"Via":          fmt.Sprintf("SIP/2.0/UDP %s;branch=z9hG4bK-%s;rport", contactHostPort(contact), randomToken(8)),
		"Max-Forwards": "70",
		"From":         from,
		"To":           to,
		"Call-ID":      callID,
		"CSeq":         fmt.Sprintf("%d REGISTER", cseq),
		"Contact":      contact + fmt.Sprintf(";expires=%d", expires),
		"Expires":      strconv.Itoa(expires),
		"User-Agent":   "h248-sip-gateway-register-probe",
	}
	if authorization != "" {
		headers["Authorization"] = authorization
	}
	return sip.BuildRequest("REGISTER", requestURI, headers, "")
}

func contactHostPort(contact string) string {
	value := strings.TrimPrefix(contact, "<sip:")
	if at := strings.IndexByte(value, '@'); at >= 0 {
		value = value[at+1:]
	}
	if end := strings.IndexAny(value, ";>"); end >= 0 {
		value = value[:end]
	}
	return value
}

func transact(ctx context.Context, connection *net.UDPConn, remote *net.UDPAddr, request, callID, method string) (*sip.Message, error) {
	if _, err := connection.WriteToUDP([]byte(request), remote); err != nil {
		return nil, err
	}
	buffer := make([]byte, 65535)
	for {
		deadline := time.Now().Add(3 * time.Second)
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}
		_ = connection.SetReadDeadline(deadline)
		count, _, err := connection.ReadFromUDP(buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		message, err := sip.Parse(buffer[:count])
		if err == nil && message.Status != 0 && message.CallID() == callID && message.CSeqMethod() == method {
			return message, nil
		}
	}
}

func routeSourceIP(remote *net.UDPAddr) (string, error) {
	probe, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		return "", err
	}
	defer probe.Close()
	local, ok := probe.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil || local.IP.IsUnspecified() {
		return "", fmt.Errorf("could not determine route source IP for %s", remote)
	}
	return local.IP.String(), nil
}

func randomToken(bytes int) string {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buffer)
}
