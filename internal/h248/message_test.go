package h248

import "testing"

func TestParseServiceChange(t *testing.T) {
	raw := []byte(`!/1 [mgc]
P=mgc{
  T=12{
    C=-{ServiceChange=root{Services{Method=Restart}}}
  }
}`)
	msg, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if msg.TransactionID != "12" || msg.ContextID != "-" {
		t.Fatalf("unexpected ids: %#v", msg)
	}
	if msg.Version != 1 || msg.TransactionType != "Transaction" {
		t.Fatalf("unexpected envelope: %#v", msg)
	}
	if len(msg.Commands) != 1 || msg.Commands[0].Name != "ServiceChange" || msg.Commands[0].TerminationID != "root" {
		t.Fatalf("unexpected commands: %#v", msg.Commands)
	}
}

func TestParseCompactServiceChangeReply(t *testing.T) {
	raw := []byte(`!/2 198.51.100.22
P=12345{C=-{SC=ROOT{SV{PF=H248/1}}}}`)
	msg, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Version != 2 || msg.MID != "198.51.100.22" {
		t.Fatalf("unexpected header: %#v", msg)
	}
	if msg.TransactionType != "Reply" || msg.TransactionID != "12345" {
		t.Fatalf("unexpected transaction: %#v", msg)
	}
	if msg.ServiceChangeProfile != "H248/1" {
		t.Fatalf("unexpected profile: %#v", msg)
	}
	if len(msg.Commands) != 1 || msg.Commands[0].Name != "ServiceChange" {
		t.Fatalf("unexpected commands: %#v", msg.Commands)
	}
}

func TestBuildServiceChangeForDeviceMID(t *testing.T) {
	message := BuildServiceChange(ServiceChangeOptions{
		Version:       1,
		MID:           "0",
		TransactionID: "99",
		Method:        "Restart",
		Reason:        "901 Cold Boot",
		Address:       "[198.51.100.10]:2944",
	})
	want := "!/1 0\nT=99{C=-{SC=ROOT{SV{MT=RS,RE=\"901 Cold Boot\",V=1,AD=[198.51.100.10]:2944}}}}\n"
	if message != want {
		t.Fatalf("unexpected ServiceChange:\n%s", message)
	}
}

func TestBuildServiceChangeIncludesDefaultVersion(t *testing.T) {
	message := BuildServiceChange(ServiceChangeOptions{
		MID:           "198.51.100.10",
		TransactionID: "100",
		Profile:       "ExampleProfile/1",
	})
	want := "!/1 [198.51.100.10]\nT=100{C=-{SC=ROOT{SV{MT=RS,RE=\"901 Cold Boot\",V=1,PF=ExampleProfile/1}}}}\n"
	if message != want {
		t.Fatalf("unexpected default ServiceChange:\n%s", message)
	}
}

func TestBuildServiceChangeWithRawDeviceName(t *testing.T) {
	message := BuildServiceChange(ServiceChangeOptions{
		Version:       1,
		MID:           "198.51.100.10",
		RawMID:        true,
		TransactionID: "101",
	})
	want := "!/1 198.51.100.10\nT=101{C=-{SC=ROOT{SV{MT=RS,RE=\"901 Cold Boot\",V=1}}}}\n"
	if message != want {
		t.Fatalf("unexpected raw device-name ServiceChange:\n%s", message)
	}
}

func TestBuildServiceChangeForPhysicalTermination(t *testing.T) {
	message := BuildServiceChange(ServiceChangeOptions{
		Version:       1,
		MID:           "[198.51.100.10]:2944",
		TransactionID: "102",
		Termination:   "A0",
	})
	want := "!/1 [198.51.100.10]:2944\nT=102{C=-{SC=A0{SV{MT=RS,RE=\"901 Cold Boot\",V=1}}}}\n"
	if message != want {
		t.Fatalf("unexpected physical-termination ServiceChange:\n%s", message)
	}
}

func TestBuildTransactionReply(t *testing.T) {
	req := &Message{Version: 3, TransactionID: "7"}
	reply := BuildReply(req, "0", []string{"C=-{SC=ROOT{}}"})
	want := "!/3 0\nP=7{\n  C=-{SC=ROOT{}}\n}\n"
	if reply != want {
		t.Fatalf("unexpected reply:\n%s", reply)
	}
}

func TestParseAdd(t *testing.T) {
	raw := []byte(`!/1 [mgc]
P=mgc{T=2{C=${Add=$ {Media{LocalControl{Mode=ReceiveOnly}}}}}}`)
	msg, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Commands[0].Name != "Add" || msg.Commands[0].TerminationID != "$" {
		t.Fatalf("unexpected command: %#v", msg.Commands[0])
	}
}

func TestParseCompactAddDoesNotTreatSDPAttributesAsCommands(t *testing.T) {
	raw := []byte("!/1 [198.51.100.20]:2944 T=7{C=${A=A0{},A=${M{L{v=0\r\nc=IN IP4 $\r\nm=audio $ RTP/AVP 8 102\r\na=ptime:20\r\na=rtpmap:102 telephone-event/8000\r\na=fmtp:102 0-15\r\n}}}}}")
	msg, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Commands) != 2 {
		t.Fatalf("commands = %#v", msg.Commands)
	}
	if msg.Commands[0].Name != "Add" || msg.Commands[0].TerminationID != "A0" {
		t.Fatalf("first command = %#v", msg.Commands[0])
	}
	if msg.Commands[1].Name != "Add" || msg.Commands[1].TerminationID != "$" {
		t.Fatalf("second command = %#v", msg.Commands[1])
	}
}

func TestParseOptionalSubtractDoesNotIncludeClosingBracesInTermination(t *testing.T) {
	msg, err := Parse([]byte(`!/1 [198.51.100.20]:2944 T=8{C=99{O-S=*}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Commands) != 1 || msg.Commands[0].Name != "Subtract" || msg.Commands[0].TerminationID != "*" {
		t.Fatalf("commands = %#v", msg.Commands)
	}
	if !msg.Commands[0].Optional || msg.Commands[0].ContextID != "99" {
		t.Fatalf("optional command metadata = %#v", msg.Commands[0])
	}
}

func TestParseMultipleContextsAndNestedDescriptors(t *testing.T) {
	raw := []byte("!/1 [198.51.100.20]:2944 T=9{" +
		"C=100{MF=A0{M{O{MO=SR}},E=11{al/*}},MF=RTP/1{M{R{v=0\r\nc=IN IP4 203.0.113.30\r\nm=audio 30000 RTP/AVP 8 102\r\na=ptime:20\r\n}}}}," +
		"C=200{O-AV=ROOT{AT{}}}}")
	msg, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Commands) != 3 {
		t.Fatalf("commands = %#v", msg.Commands)
	}
	if msg.Commands[0].ContextID != "100" || msg.Commands[0].Name != "Modify" || msg.Commands[0].TerminationID != "A0" {
		t.Fatalf("first command = %#v", msg.Commands[0])
	}
	if msg.Commands[1].ContextID != "100" || msg.Commands[1].TerminationID != "RTP/1" {
		t.Fatalf("second command = %#v", msg.Commands[1])
	}
	if msg.Commands[2].ContextID != "200" || !msg.Commands[2].Optional || msg.Commands[2].Name != "AuditValue" {
		t.Fatalf("third command = %#v", msg.Commands[2])
	}
}

func TestBuildActionReplyGroupsCommandsByContext(t *testing.T) {
	req := &Message{Version: 1, TransactionID: "4002"}
	reply := BuildActionReply(req, "[198.51.100.10]:2944", []ActionReply{{
		ContextID: "2001",
		Commands:  []string{"MF=A0", "MF=RTP/1"},
	}})
	want := "!/1 [198.51.100.10]:2944\nP=4002{\n  C=2001{MF=A0,MF=RTP/1}\n}\n"
	if reply != want {
		t.Fatalf("reply:\n%s", reply)
	}
}

func TestOptionalRequestUsesNormalCommandReply(t *testing.T) {
	command := Command{Name: "Subtract", TerminationID: "*", Optional: true}
	if got := SimpleCommandReply(command); got != "S=*" {
		t.Fatalf("optional command reply = %q", got)
	}
}
