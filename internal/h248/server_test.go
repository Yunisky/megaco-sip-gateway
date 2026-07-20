package h248

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testServerHandler struct {
	requests    atomic.Int32
	ready       chan string
	unavailable chan string
}

func (h *testServerHandler) HandleH248(_ context.Context, message *Message, responder Responder) {
	h.requests.Add(1)
	actions := []ActionReply{{ContextID: message.ContextID}}
	for _, command := range message.Commands {
		actions[0].Commands = append(actions[0].Commands, SimpleCommandReply(command))
	}
	_ = responder.Respond(BuildActionReply(message, "[127.0.0.1]:2944", actions))
}

func (h *testServerHandler) HandleH248Ready(_ context.Context, responder Responder) {
	select {
	case h.ready <- responder.RemoteAddr().String():
	default:
	}
}

func (h *testServerHandler) HandleH248Unavailable(_ context.Context, remote *net.UDPAddr) {
	select {
	case h.unavailable <- remote.String():
	default:
	}
}

func (*testServerHandler) HandleH248Response(context.Context, *Message, Responder) {}

func TestUDPServerRegistersRootAndLineAndCachesReplies(t *testing.T) {
	mgc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer mgc.Close()
	handler := &testServerHandler{ready: make(chan string, 1), unavailable: make(chan string, 1)}
	server, err := NewUDPServer(UDPServerConfig{
		Address:                   "127.0.0.1:0",
		MGC:                       mgc.LocalAddr().String(),
		MID:                       "[127.0.0.1]:2944",
		Version:                   1,
		ServiceChangeMethod:       "Restart",
		ServiceChangeReason:       "901 Cold Boot",
		ServiceChangeRetrySeconds: 1,
		PhysicalTermination:       "A0",
	}, handler, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.ListenAndServe(ctx) }()

	root, serverAddress := readH248Datagram(t, mgc)
	rootMessage, err := Parse([]byte(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(rootMessage.Commands) != 1 || rootMessage.Commands[0].TerminationID != "ROOT" {
		t.Fatalf("root ServiceChange = %#v", rootMessage.Commands)
	}
	sendServiceChangeReply(t, mgc, serverAddress, rootMessage.TransactionID, "ROOT")

	line, lineServerAddress := readH248Datagram(t, mgc)
	lineMessage, err := Parse([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if len(lineMessage.Commands) != 1 || lineMessage.Commands[0].TerminationID != "A0" {
		t.Fatalf("line ServiceChange = %#v", lineMessage.Commands)
	}
	sendServiceChangeReply(t, mgc, lineServerAddress, lineMessage.TransactionID, "A0")
	select {
	case <-handler.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not report ready")
	}

	request := "!/1 [127.0.0.2]:2944 T=700{C=99{MF=A0{E=11{al/*}},MF=RTP/1{M{O{MO=SR}}}}}}"
	if _, err := mgc.WriteToUDP([]byte(request), serverAddress); err != nil {
		t.Fatal(err)
	}
	firstReply, _ := readH248Datagram(t, mgc)
	if !strings.Contains(firstReply, "C=99{MF=A0,MF=RTP/1}") {
		t.Fatalf("first reply:\n%s", firstReply)
	}
	if _, err := mgc.WriteToUDP([]byte(request), serverAddress); err != nil {
		t.Fatal(err)
	}
	secondReply, _ := readH248Datagram(t, mgc)
	if secondReply != firstReply {
		t.Fatalf("cached reply changed:\nfirst=%s\nsecond=%s", firstReply, secondReply)
	}
	if got := handler.requests.Load(); got != 1 {
		t.Fatalf("handler requests = %d, want 1", got)
	}
}

func TestUDPServerFallsBackToBackupMGC(t *testing.T) {
	primary, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	backup, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()

	handler := &testServerHandler{ready: make(chan string, 1), unavailable: make(chan string, 1)}
	server, err := NewUDPServer(UDPServerConfig{
		Address:                   "127.0.0.1:0",
		MGC:                       primary.LocalAddr().String(),
		BackupMGC:                 backup.LocalAddr().String(),
		MID:                       "[127.0.0.1]:2944",
		Version:                   1,
		ServiceChangeRetrySeconds: 1,
		ServiceChangeMaxAttempts:  1,
		PhysicalTermination:       "A0",
	}, handler, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.ListenAndServe(ctx) }()

	// Receive but intentionally do not answer the primary registration.
	_, _ = readH248Datagram(t, primary)
	root, serverAddress := readH248Datagram(t, backup)
	rootMessage, err := Parse([]byte(root))
	if err != nil {
		t.Fatal(err)
	}
	sendServiceChangeReply(t, backup, serverAddress, rootMessage.TransactionID, "ROOT")
	line, serverAddress := readH248Datagram(t, backup)
	lineMessage, err := Parse([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	sendServiceChangeReply(t, backup, serverAddress, lineMessage.TransactionID, "A0")

	select {
	case <-handler.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not register with backup MGC")
	}
}

func TestUDPServerRuntimeFailoverToBackupMGC(t *testing.T) {
	primary, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	backup, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	handler := &testServerHandler{ready: make(chan string, 2), unavailable: make(chan string, 2)}
	server, err := NewUDPServer(UDPServerConfig{
		Address:                   "127.0.0.1:0",
		MGC:                       primary.LocalAddr().String(),
		BackupMGC:                 backup.LocalAddr().String(),
		MID:                       "[127.0.0.1]:2944",
		Version:                   1,
		ServiceChangeRetrySeconds: 1,
		ServiceChangeMaxAttempts:  1,
		MGCFailureTimeoutSeconds:  1,
		PhysicalTermination:       "A0",
	}, handler, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.ListenAndServe(ctx) }()

	root, serverAddress := readH248Datagram(t, primary)
	rootMessage, err := Parse([]byte(root))
	if err != nil {
		t.Fatal(err)
	}
	sendServiceChangeReply(t, primary, serverAddress, rootMessage.TransactionID, "ROOT")
	line, serverAddress := readH248Datagram(t, primary)
	lineMessage, err := Parse([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	sendServiceChangeReply(t, primary, serverAddress, lineMessage.TransactionID, "A0")
	select {
	case ready := <-handler.ready:
		if ready != primary.LocalAddr().String() {
			t.Fatalf("active MGC = %s, want primary %s", ready, primary.LocalAddr())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("primary MGC did not become active")
	}

	backupRoot, backupServerAddress := readH248Datagram(t, backup)
	backupRootMessage, err := Parse([]byte(backupRoot))
	if err != nil {
		t.Fatal(err)
	}
	request := "!/1 [127.0.0.2]:2944 T=998{C=-{MF=A0{E=1{al/*}}}}"
	if _, err := primary.WriteToUDP([]byte(request), serverAddress); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := handler.requests.Load(); got != 0 {
		t.Fatalf("old primary request reached handler during backup registration: %d", got)
	}
	sendServiceChangeReply(t, backup, backupServerAddress, backupRootMessage.TransactionID, "ROOT")
	backupLine, backupServerAddress := readH248Datagram(t, backup)
	backupLineMessage, err := Parse([]byte(backupLine))
	if err != nil {
		t.Fatal(err)
	}
	sendServiceChangeReply(t, backup, backupServerAddress, backupLineMessage.TransactionID, "A0")
	select {
	case unavailable := <-handler.unavailable:
		if unavailable != primary.LocalAddr().String() {
			t.Fatalf("unavailable MGC = %s, want primary %s", unavailable, primary.LocalAddr())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("primary MGC timeout was not reported")
	}
	select {
	case ready := <-handler.ready:
		if ready != backup.LocalAddr().String() {
			t.Fatalf("active MGC = %s, want backup %s", ready, backup.LocalAddr())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backup MGC did not become active")
	}

	request = "!/1 [127.0.0.2]:2944 T=999{C=-{MF=A0{E=1{al/*}}}}"
	if _, err := primary.WriteToUDP([]byte(request), serverAddress); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := handler.requests.Load(); got != 0 {
		t.Fatalf("non-active primary request reached handler: %d", got)
	}
}

func readH248Datagram(t *testing.T, connection *net.UDPConn) (string, *net.UDPAddr) {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 65535)
	count, remote, err := connection.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	return string(buffer[:count]), remote
}

func sendServiceChangeReply(t *testing.T, connection *net.UDPConn, remote *net.UDPAddr, transactionID, termination string) {
	t.Helper()
	reply := "!/1 [127.0.0.2]:2944\nP=" + transactionID + "{C=-{SC=" + termination + "{SV{V=1}}}}"
	if _, err := connection.WriteToUDP([]byte(reply), remote); err != nil {
		t.Fatal(err)
	}
}
