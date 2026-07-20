package sip

import "testing"

func TestParseInvite(t *testing.T) {
	raw := []byte("INVITE sip:5550100@example.com SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 192.0.2.1;branch=z9hG4bK1\r\n" +
		"From: <sip:a@example.com>;tag=1\r\n" +
		"To: <sip:b@example.com>\r\n" +
		"Call-ID: abc\r\n" +
		"CSeq: 7 INVITE\r\n" +
		"Content-Length: 0\r\n\r\n")
	msg, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Method != "INVITE" || msg.URI != "sip:5550100@example.com" {
		t.Fatalf("unexpected request: %#v", msg)
	}
	if msg.CallID() != "abc" || msg.CSeqMethod() != "INVITE" || msg.CSeqNumber() != "7" {
		t.Fatalf("unexpected identifiers: call=%s cseq=%s %s", msg.CallID(), msg.CSeqNumber(), msg.CSeqMethod())
	}
}

func TestBuildResponse(t *testing.T) {
	req, err := Parse([]byte("OPTIONS sip:x SIP/2.0\r\nVia: SIP/2.0/UDP a\r\nFrom: <sip:a>;tag=1\r\nTo: <sip:b>\r\nCall-ID: c\r\nCSeq: 1 OPTIONS\r\nContent-Length: 0\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	resp := BuildResponse(req, 200, "OK", "", "", nil)
	if want := "SIP/2.0 200 OK"; resp[:len(want)] != want {
		t.Fatalf("missing status line: %q", resp)
	}
}
