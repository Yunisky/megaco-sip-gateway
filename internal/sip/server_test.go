package sip

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

type cachingTestHandler struct {
	requests atomic.Int32
	release  chan struct{}
}

func (h *cachingTestHandler) HandleSIP(_ context.Context, message *Message, responder Responder) {
	h.requests.Add(1)
	<-h.release
	_ = responder.Respond(BuildResponse(message, 100, "Trying", "", "", nil))
	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = responder.Respond(BuildResponse(message, 200, "OK", "", "sip:gateway@127.0.0.1", nil))
	}()
}

func TestUDPServerCachesLatestResponseForDuplicateRequest(t *testing.T) {
	handler := &cachingTestHandler{release: make(chan struct{})}
	server, err := NewUDPServer("127.0.0.1:0", "", handler, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.ListenAndServe(ctx) }()

	var target *net.UDPAddr
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if address, ok := server.LocalAddr().(*net.UDPAddr); ok && address != nil {
			target = address
			break
		}
		time.Sleep(time.Millisecond)
	}
	if target == nil {
		t.Fatal("SIP server did not start")
	}
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request := "INVITE sip:1000@127.0.0.1 SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 127.0.0.1;branch=z9hG4bK-cache-test\r\n" +
		"From: <sip:a@localhost>;tag=1\r\n" +
		"To: <sip:1000@localhost>\r\n" +
		"Call-ID: cache-test\r\n" +
		"CSeq: 1 INVITE\r\n" +
		"Content-Length: 0\r\n\r\n"
	if _, err := client.WriteToUDP([]byte(request), target); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && handler.requests.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if handler.requests.Load() != 1 {
		t.Fatalf("initial handler requests = %d, want 1", handler.requests.Load())
	}
	if _, err := client.WriteToUDP([]byte(request), target); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if handler.requests.Load() != 1 {
		t.Fatalf("in-flight duplicate reached handler: requests=%d", handler.requests.Load())
	}
	close(handler.release)
	first := readSIPDatagram(t, client)
	second := readSIPDatagram(t, client)
	if !strings.HasPrefix(first, "SIP/2.0 100 Trying") || !strings.HasPrefix(second, "SIP/2.0 200 OK") {
		t.Fatalf("responses:\n%s\n%s", first, second)
	}
	if _, err := client.WriteToUDP([]byte(request), target); err != nil {
		t.Fatal(err)
	}
	cached := readSIPDatagram(t, client)
	if cached != second {
		t.Fatalf("cached response changed:\nwant=%s\ngot=%s", second, cached)
	}
	if got := handler.requests.Load(); got != 1 {
		t.Fatalf("handler requests = %d, want 1", got)
	}
}

func TestUDPServerExpiresStuckInFlightAndStaleCachedEntries(t *testing.T) {
	server, err := NewUDPServer("127.0.0.1:0", "", &cachingTestHandler{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	server.inFlight["stuck"] = time.Now().Add(-time.Second)
	if !server.beginTransaction("stuck") {
		t.Fatal("expired in-flight transaction was not admitted")
	}
	server.responseCache["stale"] = cachedResponse{message: "old", expiresAt: time.Now().Add(-time.Second)}
	server.cacheOrder = append(server.cacheOrder, "stale")
	if response := server.cachedResponse("stale"); response != "" {
		t.Fatalf("expired cached response = %q, want empty", response)
	}
	if len(server.cacheOrder) != 0 {
		t.Fatalf("expired cache order was not pruned: %#v", server.cacheOrder)
	}
}

func readSIPDatagram(t *testing.T, connection *net.UDPConn) string {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 65535)
	count, _, err := connection.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	return string(buffer[:count])
}
