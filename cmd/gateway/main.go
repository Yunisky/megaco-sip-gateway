package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/Yunisky/megaco-sip-gateway/internal/bridge"
	"github.com/Yunisky/megaco-sip-gateway/internal/config"
	"github.com/Yunisky/megaco-sip-gateway/internal/h248"
	"github.com/Yunisky/megaco-sip-gateway/internal/sip"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "gateway.yaml", "path to gateway config")
	checkConfig := flag.Bool("check-config", false, "validate configuration and exit without opening sockets")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		logger.Error("validate config", "error", err)
		os.Exit(1)
	}
	if *checkConfig {
		fmt.Printf("configuration valid: SIP=%s H.248=%s MGC=%s backup=%s RTP=%d-%d\n",
			cfg.SIP.Listen, cfg.H248.Listen, cfg.H248.MGC, cfg.H248.BackupMGC, cfg.Media.PortMin, cfg.Media.PortMax)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	callBridge := bridge.New(logger, cfg)
	defer callBridge.Close()

	sipServer, err := sip.NewUDPServer(cfg.SIP.Listen, cfg.SIP.BindDevice, callBridge, logger)
	if err != nil {
		logger.Error("create sip server", "error", err)
		os.Exit(1)
	}
	callBridge.SetSIPSender(sipServer)

	if cfg.H248.Transport != "udp" || cfg.H248.Encoding != "text" {
		logger.Error("unsupported h248 transport or encoding", "transport", cfg.H248.Transport, "encoding", cfg.H248.Encoding)
		os.Exit(1)
	}
	h248Server, err := h248.NewUDPServer(h248.UDPServerConfig{
		Address:                   cfg.H248.Listen,
		Device:                    cfg.H248.BindDevice,
		MGC:                       cfg.H248.MGC,
		BackupMGC:                 cfg.H248.BackupMGC,
		MID:                       cfg.H248.MGID,
		Version:                   cfg.H248.Version,
		ServiceChangeMethod:       cfg.H248.ServiceChangeMethod,
		ServiceChangeReason:       cfg.H248.ServiceChangeReason,
		ServiceChangeProfile:      cfg.H248.ServiceChangeProfile,
		ServiceChangeAddress:      cfg.H248.ServiceChangeAddress,
		ServiceChangeRetrySeconds: cfg.H248.ServiceChangeRetrySeconds,
		ServiceChangeMaxAttempts:  cfg.H248.ServiceChangeMaxAttempts,
		MGCFailureTimeoutSeconds:  cfg.H248.MGCFailureTimeoutSeconds,
		PhysicalTermination:       cfg.H248.PhysicalTermination,
	}, callBridge, logger)
	if err != nil {
		logger.Error("create h248 server", "error", err)
		os.Exit(1)
	}

	errCh := make(chan error, 2)
	go func() { errCh <- sipServer.ListenAndServe(ctx) }()
	go func() { errCh <- h248Server.ListenAndServe(ctx) }()

	logger.Info("gateway started",
		"version", version,
		"sip_listen", cfg.SIP.Listen,
		"sip_bind_device", cfg.SIP.BindDevice,
		"h248_listen", cfg.H248.Listen,
		"h248_bind_device", cfg.H248.BindDevice,
		"h248_transport", cfg.H248.Transport,
		"h248_encoding", cfg.H248.Encoding,
		"h248_version", cfg.H248.Version,
		"h248_mgc", cfg.H248.MGC,
		"mg_id", cfg.H248.MGID,
		"rtp_ip", cfg.Media.RTPIP,
	)

	select {
	case <-ctx.Done():
		logger.Info("gateway stopping")
	case err := <-errCh:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			logger.Error("gateway failed", "error", err)
			os.Exit(1)
		}
	}
}
