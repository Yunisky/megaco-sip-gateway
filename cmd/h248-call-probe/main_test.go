package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Yunisky/megaco-sip-gateway/internal/h248"
)

func TestExtractEventRequestID(t *testing.T) {
	raw := `!/1 [198.51.100.20]:2944 T=1{C=-{MF=*{E=3005{al/*}}}}`
	if got := extractEventRequestID(raw); got != "3005" {
		t.Fatalf("request ID = %q", got)
	}
}

func TestBuildNotify(t *testing.T) {
	got := buildNotify("[198.51.100.10]:2944", "22", "-", "A0", "3005", "20250101T22450012", "al/of")
	want := "!/1 [198.51.100.10]:2944\nT=22{C=-{N=A0{OE=3005{20250101T22450012:al/of}}}}\n"
	if got != want {
		t.Fatalf("Notify:\n%s", got)
	}
}

func TestBuildNotifyInCallContext(t *testing.T) {
	got := buildNotify("[198.51.100.10]:2944", "24", "2002", "A0", "3002", "20250102T00120338", "al/of")
	want := "!/1 [198.51.100.10]:2944\nT=24{C=2002{N=A0{OE=3002{20250102T00120338:al/of}}}}\n"
	if got != want {
		t.Fatalf("context Notify:\n%s", got)
	}
}

func TestFormatH248Time(t *testing.T) {
	value := time.Date(2025, 1, 1, 22, 45, 0, 120_000_000, time.FixedZone("CST", 8*60*60))
	if got := formatH248Time(value); got != "20250101T22450012" {
		t.Fatalf("time notation = %q", got)
	}
}

func TestFormatObservedEvent(t *testing.T) {
	tests := map[string]string{
		"plain":      "al/of",
		"paren-init": "al/of(init=false)",
		"brace-init": "al/of{init=false}",
	}
	for style, want := range tests {
		got, err := formatObservedEvent("al/of", style)
		if err != nil {
			t.Fatalf("style %s: %v", style, err)
		}
		if got != want {
			t.Fatalf("style %s = %q, want %q", style, got, want)
		}
	}
}

func TestBuildDigitMapCompleteNotify(t *testing.T) {
	got := buildDigitMapCompleteNotify(
		"[198.51.100.10]:2944",
		"23",
		"A0",
		"3002",
		"20250101T23500000",
		"15550123456",
	)
	want := "!/1 [198.51.100.10]:2944\nT=23{C=-{N=A0{OE=3002{20250101T23500000:dd/ce{ds=\"15550123456\",Meth=UM}}}}}\n"
	if got != want {
		t.Fatalf("digit-map Notify:\n%s", got)
	}
}

func TestContainsRingSignal(t *testing.T) {
	if !containsRingSignal(`T=1{C=20{A=A0{SG{al/ri}}}}`) {
		t.Fatal("al/ri ring signal not detected")
	}
	if containsRingSignal(`T=1{C=-{MF=A0{SG{}}}}`) {
		t.Fatal("empty signals descriptor detected as ringing")
	}
	if !containsRingSignal(`T=2{C=7{MF=A0{SG{andisp/dwa{ddb=04,pattern=1}}}}}`) {
		t.Fatal("andisp/dwa signal not detected")
	}
}

func TestBuildIncomingAddReply(t *testing.T) {
	got := buildIncomingAddReply(
		"[198.51.100.10]:2944",
		"4001",
		"1001",
		"A0",
		"RTP/1",
		"198.51.100.10",
		4000,
	)
	wants := []string{
		"P=4001{C=1001{A=A0,A=RTP/1{",
		"c=IN IP4 198.51.100.10\r\n",
		"m=audio 4000 RTP/AVP 8 102\r\n",
		"a=rtpmap:102 telephone-event/8000\r\n",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("Add reply does not contain %q:\n%s", want, got)
		}
	}
}

func TestIsCallSetupAdd(t *testing.T) {
	msg, err := h248.Parse([]byte("!/1 [198.51.100.20]:2944 T=7{C=${A=A0{},A=${M{L{v=0\r\na=ptime:20\r\n}}}}}"))
	if err != nil {
		t.Fatal(err)
	}
	if !isCallSetupAdd(msg, "A0") {
		t.Fatalf("call setup not detected: %#v", msg.Commands)
	}
}

func TestBuildCommandReply(t *testing.T) {
	request, err := h248.Parse([]byte(`!/1 [198.51.100.20]:2944
T=4003{C=-{MF=*{E=3005{al/*}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	reply, err := buildCommandReply(request, "[198.51.100.10]:2944")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "P=4003") || !strings.Contains(reply, "C=-{MF=*}") {
		t.Fatalf("reply:\n%s", reply)
	}
}

func TestBuildCommandReplyGroupsMultipleCommandsAndContexts(t *testing.T) {
	request, err := h248.Parse([]byte(`!/1 [198.51.100.20]:2944 T=9{C=100{MF=A0{},MF=RTP/1{}},C=-{O-AV=ROOT{AT{}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	reply, err := buildCommandReply(request, "[198.51.100.10]:2944")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "C=100{MF=A0,MF=RTP/1}") || !strings.Contains(reply, "C=-{AV=ROOT}") {
		t.Fatalf("reply:\n%s", reply)
	}
}
