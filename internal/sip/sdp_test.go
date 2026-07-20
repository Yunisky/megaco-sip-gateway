package sip

import (
	"strings"
	"testing"
)

func TestBuildSDPWithTelephoneEvent(t *testing.T) {
	sdp := BuildSDP("h248gw", "192.0.2.10", 4000, []string{"PCMA/8000", "PCMU/8000"}, 97)

	wants := []string{
		"c=IN IP4 192.0.2.10\r\n",
		"m=audio 4000 RTP/AVP 8 0 97\r\n",
		"a=rtpmap:97 telephone-event/8000\r\n",
		"a=fmtp:97 0-16\r\n",
	}
	for _, want := range wants {
		if !strings.Contains(sdp, want) {
			t.Fatalf("SDP does not contain %q:\n%s", want, sdp)
		}
	}
}

func TestBuildSDPOmitsInvalidDTMFPayloadType(t *testing.T) {
	sdp := BuildSDP("h248gw", "127.0.0.1", 20000, []string{"PCMA/8000"}, 128)
	if strings.Contains(sdp, "telephone-event") {
		t.Fatalf("invalid DTMF payload type included:\n%s", sdp)
	}
	if !strings.Contains(sdp, "m=audio 20000 RTP/AVP 8\r\n") {
		t.Fatalf("unexpected media line:\n%s", sdp)
	}
}

func TestParseSDPAudioAndTelephoneEvent(t *testing.T) {
	body := "v=0\r\n" +
		"o=pbx 1 1 IN IP4 192.0.2.20\r\n" +
		"c=IN IP4 192.0.2.20\r\n" +
		"m=audio 18000 RTP/AVP 8 101\r\n" +
		"a=rtpmap:8 PCMA/8000\r\n" +
		"a=rtpmap:101 telephone-event/8000\r\n" +
		"a=ptime:20\r\n" +
		"a=sendrecv\r\n"
	sdp, err := ParseSDP(body)
	if err != nil {
		t.Fatal(err)
	}
	if sdp.IP != "192.0.2.20" || sdp.Port != 18000 || sdp.TelephoneEventPayload != 101 || sdp.PacketizationTime != 20 {
		t.Fatalf("SDP = %#v", sdp)
	}
	if len(sdp.Codecs) != 1 || sdp.Codecs[0] != "PCMA/8000" {
		t.Fatalf("codecs = %#v", sdp.Codecs)
	}
}
