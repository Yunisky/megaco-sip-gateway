package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/Yunisky/megaco-sip-gateway/internal/h248"
	"github.com/Yunisky/megaco-sip-gateway/internal/socketbind"
)

func main() {
	target := flag.String("target", "127.0.0.1:2944", "gateway H.248 UDP address")
	source := flag.String("source", ":0", "local UDP address; use IP:2944 for a carrier probe")
	bindDevice := flag.String("bind-device", "", "Linux interface or VRF device for the probe socket")
	serviceChange := flag.Bool("service-change", false, "send an MG ServiceChange/Restart instead of the local smoke-test sequence")
	version := flag.Int("version", 1, "H.248 protocol and ServiceChange version")
	mid := flag.String("mid", "0", "MG mId/MGID")
	rawMID := flag.Bool("raw-mid", false, "emit mId exactly as supplied, for vendor DeviceName compatibility")
	profile := flag.String("profile", "", "ServiceChange profile, for example ExampleProfile/1")
	advertisedAddress := flag.String("advertised-address", "", "ServiceChange address, for example [MG_IP]:H248_PORT")
	method := flag.String("method", "Restart", "ServiceChange method")
	reason := flag.String("reason", "901 Cold Boot", "ServiceChange reason")
	transactionID := flag.String("transaction", "", "transaction ID; defaults to a time-derived value")
	count := flag.Int("count", 3, "number of ServiceChange transmissions")
	timeout := flag.Duration("timeout", 3*time.Second, "reply timeout after each transmission")
	flag.Parse()

	remote, err := net.ResolveUDPAddr("udp", *target)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := socketbind.ListenUDP(context.Background(), *source, *bindDevice)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	if *serviceChange {
		probeServiceChange(conn, remote, serviceChangeProbeOptions{
			version:           *version,
			mid:               *mid,
			rawMID:            *rawMID,
			profile:           *profile,
			advertisedAddress: *advertisedAddress,
			method:            *method,
			reason:            *reason,
			transactionID:     *transactionID,
			count:             *count,
			timeout:           *timeout,
		})
		return
	}

	messages := []string{
		`!/1 [mgc.example]
P=mgc.example{
  T=1{
    C=-{ServiceChange=root{Services{Method=Restart}}}
  }
}`,
		`!/1 [mgc.example]
P=mgc.example{
  T=2{
    C=${Add=$ {Media{LocalControl{Mode=ReceiveOnly}}}}
  }
}`,
		`!/1 [mgc.example]
P=mgc.example{
  T=3{
    C=1001{Modify=rtp/1 {Media{LocalControl{Mode=SendReceive}}}}
  }
}`,
		`!/1 [mgc.example]
P=mgc.example{
  T=4{
    C=1001{Subtract=rtp/1 {}}
  }
}`,
	}

	buf := make([]byte, 8192)
	for _, msg := range messages {
		if _, err := conn.WriteToUDP([]byte(msg), remote); err != nil {
			log.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("---- H.248 reply ----\n%s\n", string(buf[:n]))
	}
}

type serviceChangeProbeOptions struct {
	version           int
	mid               string
	rawMID            bool
	profile           string
	advertisedAddress string
	method            string
	reason            string
	transactionID     string
	count             int
	timeout           time.Duration
}

func probeServiceChange(conn *net.UDPConn, remote *net.UDPAddr, options serviceChangeProbeOptions) {
	transactionID := options.transactionID
	if transactionID == "" {
		transactionID = strconv.FormatInt(time.Now().UnixNano()&0x7fffffff, 10)
	}
	message := h248.BuildServiceChange(h248.ServiceChangeOptions{
		Version:       options.version,
		MID:           options.mid,
		RawMID:        options.rawMID,
		TransactionID: transactionID,
		Method:        options.method,
		Reason:        options.reason,
		Profile:       options.profile,
		Address:       options.advertisedAddress,
	})
	if options.count <= 0 {
		options.count = 1
	}
	if options.timeout <= 0 {
		options.timeout = 3 * time.Second
	}

	fmt.Printf("H.248 ServiceChange probe %s -> %s\n%s", conn.LocalAddr(), remote, message)
	buf := make([]byte, 65535)
	for attempt := 1; attempt <= options.count; attempt++ {
		if _, err := conn.WriteToUDP([]byte(message), remote); err != nil {
			log.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(options.timeout))
		n, source, err := conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				fmt.Printf("attempt %d/%d: no reply within %s\n", attempt, options.count, options.timeout)
				continue
			}
			log.Fatal(err)
		}
		fmt.Printf("---- H.248 reply from %s ----\n%s\n", source, string(buf[:n]))
		if parsed, err := h248.Parse(buf[:n]); err == nil {
			fmt.Printf("parsed: version=%d mid=%s type=%s transaction=%s profile=%s error=%s\n",
				parsed.Version,
				parsed.MID,
				parsed.TransactionType,
				parsed.TransactionID,
				parsed.ServiceChangeProfile,
				parsed.ErrorCode,
			)
		}
		return
	}
	fmt.Fprintln(os.Stderr, "no H.248 response received")
	os.Exit(2)
}
