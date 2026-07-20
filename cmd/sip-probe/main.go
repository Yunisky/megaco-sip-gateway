package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Yunisky/megaco-sip-gateway/internal/sip"
	"github.com/Yunisky/megaco-sip-gateway/internal/socketbind"
)

type options struct {
	Target       string
	Source       string
	AdvertisedIP string
	RTPSource    string
	BindDevice   string
	Number       string
	Hold         time.Duration
	Timeout      time.Duration
}

func main() {
	var cfg options
	flag.StringVar(&cfg.Target, "target", "127.0.0.1:5060", "gateway SIP UDP address")
	flag.StringVar(&cfg.Source, "source", "127.0.0.1:0", "local SIP UDP address")
	flag.StringVar(&cfg.AdvertisedIP, "advertised-ip", "127.0.0.1", "IP placed in SIP and SDP")
	flag.StringVar(&cfg.RTPSource, "rtp-source", "127.0.0.1:40000", "local RTP UDP address")
	flag.StringVar(&cfg.BindDevice, "bind-device", "", "Linux interface or VRF device for SIP/RTP sockets")
	flag.StringVar(&cfg.Number, "number", "1000", "authorized lab destination number")
	flag.DurationVar(&cfg.Hold, "hold", 10*time.Second, "time to hold the call after 200 OK")
	flag.DurationVar(&cfg.Timeout, "timeout", 60*time.Second, "overall probe timeout")
	flag.Parse()
	if cfg.Number == "" || strings.Trim(cfg.Number, "0123456789*#") != "" {
		log.Fatal("number must contain only 0-9, *, and #")
	}
	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

func run(cfg options) error {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	remote, err := net.ResolveUDPAddr("udp", cfg.Target)
	if err != nil {
		return err
	}
	signaling, err := socketbind.ListenUDP(ctx, cfg.Source, cfg.BindDevice)
	if err != nil {
		return err
	}
	defer signaling.Close()
	rtpConn, err := socketbind.ListenUDP(ctx, cfg.RTPSource, cfg.BindDevice)
	if err != nil {
		return err
	}
	defer rtpConn.Close()

	localSIP := signaling.LocalAddr().(*net.UDPAddr)
	localRTP := rtpConn.LocalAddr().(*net.UDPAddr)
	callID := fmt.Sprintf("probe-%d@%s", time.Now().UnixNano(), cfg.AdvertisedIP)
	requestURI := fmt.Sprintf("sip:%s@%s", cfg.Number, remote.String())
	from := fmt.Sprintf("<sip:probe@%s>;tag=probe-%d", cfg.AdvertisedIP, time.Now().UnixNano())
	to := fmt.Sprintf("<sip:%s@%s>", cfg.Number, remote.String())
	contact := fmt.Sprintf("<sip:probe@%s>", net.JoinHostPort(cfg.AdvertisedIP, strconv.Itoa(localSIP.Port)))
	offer := sip.BuildSDP("probe", cfg.AdvertisedIP, localRTP.Port, []string{"PCMA/8000"}, 97)
	invite := sip.BuildRequest("INVITE", requestURI, map[string]string{
		"Via":          fmt.Sprintf("SIP/2.0/UDP %s;branch=z9hG4bK-probe-invite;rport", net.JoinHostPort(cfg.AdvertisedIP, strconv.Itoa(localSIP.Port))),
		"Max-Forwards": "70",
		"From":         from,
		"To":           to,
		"Call-ID":      callID,
		"CSeq":         "1 INVITE",
		"Contact":      contact,
	}, offer)
	if _, err := signaling.WriteToUDP([]byte(invite), remote); err != nil {
		return err
	}
	fmt.Printf("SIP INVITE sent: call-id=%s destination=%s RTP=%s\n", callID, cfg.Number, localRTP)

	var receivedRTP atomic.Uint64
	go receiveRTP(ctx, rtpConn, &receivedRTP)
	mediaCtx, mediaCancel := context.WithCancel(ctx)
	defer mediaCancel()
	var mediaRemote *net.UDPAddr
	answered := false
	byeSent := false
	hangupAt := time.Time{}
	remoteTo := to
	buffer := make([]byte, 65535)

	for {
		if answered && !byeSent && !hangupAt.IsZero() && !time.Now().Before(hangupAt) {
			bye := sip.BuildRequest("BYE", requestURI, map[string]string{
				"Via":          fmt.Sprintf("SIP/2.0/UDP %s;branch=z9hG4bK-probe-bye;rport", net.JoinHostPort(cfg.AdvertisedIP, strconv.Itoa(localSIP.Port))),
				"Max-Forwards": "70",
				"From":         from,
				"To":           remoteTo,
				"Call-ID":      callID,
				"CSeq":         "2 BYE",
			}, "")
			if _, err := signaling.WriteToUDP([]byte(bye), remote); err != nil {
				return err
			}
			byeSent = true
			fmt.Println("SIP BYE sent")
		}

		deadline := time.Now().Add(250 * time.Millisecond)
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}
		_ = signaling.SetReadDeadline(deadline)
		count, source, err := signaling.ReadFromUDP(buffer)
		if err != nil {
			if netError, ok := err.(net.Error); ok && netError.Timeout() {
				if ctx.Err() != nil {
					return fmt.Errorf("probe timed out; received RTP packets=%d: %w", receivedRTP.Load(), ctx.Err())
				}
				continue
			}
			if errors.Is(err, net.ErrClosed) && ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		message, err := sip.Parse(buffer[:count])
		if err != nil || message.CallID() != callID || message.Status == 0 {
			continue
		}
		fmt.Printf("SIP response: %d %s from %s\n", message.Status, message.Reason, source)
		if message.CSeqMethod() == "BYE" && message.Status >= 200 {
			fmt.Printf("probe complete: received RTP packets=%d\n", receivedRTP.Load())
			return nil
		}
		if message.CSeqMethod() != "INVITE" {
			continue
		}
		if message.Body != "" {
			description, parseErr := sip.ParseSDP(message.Body)
			if parseErr == nil {
				candidate, resolveErr := net.ResolveUDPAddr("udp", net.JoinHostPort(description.IP, strconv.Itoa(description.Port)))
				if resolveErr == nil && mediaRemote == nil {
					mediaRemote = candidate
					go sendPCMASilence(mediaCtx, rtpConn, mediaRemote)
					fmt.Printf("RTP peer learned: %s\n", mediaRemote)
				}
			}
		}
		if message.Status >= 200 && message.Status < 300 && !answered {
			remoteTo = message.Header("To")
			ack := sip.BuildRequest("ACK", requestURI, map[string]string{
				"Via":          fmt.Sprintf("SIP/2.0/UDP %s;branch=z9hG4bK-probe-ack;rport", net.JoinHostPort(cfg.AdvertisedIP, strconv.Itoa(localSIP.Port))),
				"Max-Forwards": "70",
				"From":         from,
				"To":           remoteTo,
				"Call-ID":      callID,
				"CSeq":         "1 ACK",
			}, "")
			if _, err := signaling.WriteToUDP([]byte(ack), remote); err != nil {
				return err
			}
			answered = true
			hangupAt = time.Now().Add(cfg.Hold)
			fmt.Printf("SIP ACK sent; holding for %s\n", cfg.Hold)
		} else if message.Status >= 300 {
			return fmt.Errorf("INVITE failed: %d %s", message.Status, message.Reason)
		}
	}
}

func receiveRTP(ctx context.Context, connection *net.UDPConn, count *atomic.Uint64) {
	buffer := make([]byte, 65535)
	for {
		_ = connection.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if _, _, err := connection.ReadFromUDP(buffer); err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		count.Add(1)
	}
}

func sendPCMASilence(ctx context.Context, connection *net.UDPConn, remote *net.UDPAddr) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	packet := make([]byte, 12+160)
	packet[0] = 0x80
	packet[1] = 8
	for index := 12; index < len(packet); index++ {
		packet[index] = 0xd5
	}
	var sequence uint16
	var timestamp uint32
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sequence++
			timestamp += 160
			packet[2] = byte(sequence >> 8)
			packet[3] = byte(sequence)
			packet[4] = byte(timestamp >> 24)
			packet[5] = byte(timestamp >> 16)
			packet[6] = byte(timestamp >> 8)
			packet[7] = byte(timestamp)
			_, _ = connection.WriteToUDP(packet, remote)
		}
	}
}
