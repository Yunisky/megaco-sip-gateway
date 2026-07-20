package h248

import (
	"testing"
	"time"
)

func TestBuildNotifyInAllocatedContext(t *testing.T) {
	observedAt := time.Date(2025, 1, 2, 0, 21, 45, 560_000_000, time.UTC)
	got := BuildNotify(1, "[198.51.100.10]:2944", "22", "2001", "A0", "3004", observedAt, "al/of")
	want := "!/1 [198.51.100.10]:2944\nT=22{C=2001{N=A0{OE=3004{20250102T00214556:al/of}}}}\n"
	if got != want {
		t.Fatalf("Notify:\n%s", got)
	}
}

func TestBuildDigitMapCompleteNotify(t *testing.T) {
	observedAt := time.Date(2025, 1, 2, 2, 31, 29, 940_000_000, time.UTC)
	got := BuildDigitMapCompleteNotify(1, "[198.51.100.10]:2944", "23", "-", "A0", "3004", observedAt, "15550123456")
	want := "!/1 [198.51.100.10]:2944\nT=23{C=-{N=A0{OE=3004{20250102T02312994:dd/ce{ds=\"15550123456\",Meth=UM}}}}}\n"
	if got != want {
		t.Fatalf("digit-map Notify:\n%s", got)
	}
}

func TestDecodeAndispCallerID(t *testing.T) {
	raw := `MF=A0{SG{andisp/dwa{ddb=04133031313230303231313535353031323334353624,pattern=1}}}`
	if got := DecodeAndispCallerID(raw); got != "15550123456" {
		t.Fatalf("caller ID = %q", got)
	}
}

func TestExtractRemoteMediaFromAdd(t *testing.T) {
	raw := "A=${M{O{MO=IN},L{v=0\r\nc=IN IP4 $\r\nm=audio $ RTP/AVP 8 102\r\n},R{v=0\r\nc=IN IP4 203.0.113.30\r\nm=audio 30000 RTP/AVP 8 102\r\na=rtpmap:102 telephone-event/8000\r\n}}}"
	media, ok := ExtractRemoteMedia(raw)
	if !ok {
		t.Fatal("remote media not found")
	}
	if media.IP != "203.0.113.30" || media.Port != 30000 || media.TelephoneEventPayload != 102 {
		t.Fatalf("media = %#v", media)
	}
	if len(media.PayloadTypes) != 2 || media.PayloadTypes[0] != 8 || media.PayloadTypes[1] != 102 {
		t.Fatalf("payloads = %#v", media.PayloadTypes)
	}
}

func TestExtractLocalTelephoneEventPayloadFromWildcardOffer(t *testing.T) {
	raw := "A=${M{O{MO=RC},L{v=0\r\nc=IN IP4 $\r\nm=audio $ RTP/AVP 8 97\r\na=ptime:20\r\na=rtpmap:97 telephone-event/8000\r\na=fmtp:97 0-15\r\n}}}"
	payload, ok := ExtractLocalTelephoneEventPayload(raw)
	if !ok || payload != 97 {
		t.Fatalf("local telephone-event payload = %d, %v", payload, ok)
	}
}

func TestExtractLocalTelephoneEventPayloadWithEscapedNewlines(t *testing.T) {
	raw := `A=${M{L{v=0\r\nc=IN IP4 $\r\nm=audio $ RTP/AVP 8 97\r\na=rtpmap:97 telephone-event/8000\r\n}}}`
	payload, ok := ExtractLocalTelephoneEventPayload(raw)
	if !ok || payload != 97 {
		t.Fatalf("escaped local telephone-event payload = %d, %v", payload, ok)
	}
}
