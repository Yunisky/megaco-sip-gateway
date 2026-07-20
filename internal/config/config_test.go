package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBindDevices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	data := []byte(`sip:
  listen: "192.0.2.10:5060"
  bind_device: "sip0"
  advertised_address: "192.0.2.10:5060"
  outbound_proxy: "192.0.2.20:5060"
  user_agent: "test-gateway"
h248:
  listen: "198.51.100.10:2944"
  bind_device: "vrf-h248"
  transport: "udp"
  encoding: "text"
  version: 2
  mg_id: "0"
  mgc: "198.51.100.22:2944"
  backup_mgc: "198.51.100.21:2944"
  service_change_method: "Restart"
  service_change_reason: "901 Cold Boot"
  service_change_profile: "ExampleProfile/1"
  service_change_address: "[198.51.100.10]:2944"
  service_change_retry_seconds: 7
  service_change_max_attempts: 4
  mgc_failure_timeout_seconds: 420
  origination_stabilization_seconds: 6
  digit_report_delay_milliseconds: 250
  physical_termination: "aaln/1"
  ephemeral_termination_prefix: "RTP/"
media:
  rtp_ip: "192.0.2.10"
  h248_rtp_ip: "198.51.100.10"
  port_min: 4000
  port_max: 4998
  dtmf_payload_type: 97
  h248_dtmf_payload_type: 102
  h248_dtmf_mode: inband
  ptime_ms: 20
  codecs: [PCMA/8000, PCMU/8000]
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SIP.BindDevice != "sip0" {
		t.Fatalf("SIP bind device = %q", cfg.SIP.BindDevice)
	}
	if cfg.SIP.AdvertisedAddress != "192.0.2.10:5060" || cfg.SIP.OutboundProxy != "192.0.2.20:5060" || cfg.SIP.UserAgent != "test-gateway" {
		t.Fatalf("SIP interop = %#v", cfg.SIP)
	}
	if cfg.H248.BindDevice != "vrf-h248" {
		t.Fatalf("H.248 bind device = %q", cfg.H248.BindDevice)
	}
	if cfg.H248.Version != 2 || cfg.H248.MGC != "198.51.100.22:2944" || cfg.H248.BackupMGC != "198.51.100.21:2944" {
		t.Fatalf("H.248 endpoint = %#v", cfg.H248)
	}
	if cfg.H248.ServiceChangeProfile != "ExampleProfile/1" {
		t.Fatalf("H.248 profile = %q", cfg.H248.ServiceChangeProfile)
	}
	if cfg.H248.MGID != "0" || cfg.H248.ServiceChangeAddress != "[198.51.100.10]:2944" || cfg.H248.ServiceChangeRetrySeconds != 7 {
		t.Fatalf("H.248 service change = %#v", cfg.H248)
	}
	if cfg.H248.ServiceChangeMaxAttempts != 4 {
		t.Fatalf("H.248 ServiceChange attempts = %d", cfg.H248.ServiceChangeMaxAttempts)
	}
	if cfg.H248.MGCFailureTimeoutSeconds != 420 {
		t.Fatalf("H.248 MGC timeout = %d", cfg.H248.MGCFailureTimeoutSeconds)
	}
	if cfg.H248.OriginationStabilizationSeconds != 6 || cfg.H248.DigitReportDelayMilliseconds != 250 {
		t.Fatalf("H.248 origination timing = %#v", cfg.H248)
	}
	if cfg.H248.PhysicalTermination != "aaln/1" || cfg.H248.EphemeralTerminationPrefix != "RTP/" {
		t.Fatalf("H.248 terminations = %#v", cfg.H248)
	}
	if cfg.Media.RTPIP != "192.0.2.10" || cfg.Media.H248RTPIP != "198.51.100.10" {
		t.Fatalf("media addresses = %#v", cfg.Media)
	}
	if cfg.Media.PortMin != 4000 || cfg.Media.PortMax != 4998 || cfg.Media.DTMFPayloadType != 97 {
		t.Fatalf("media profile = %#v", cfg.Media)
	}
	if cfg.Media.H248DTMFPayloadType != 102 || cfg.Media.H248DTMFMode != "inband" || cfg.Media.PacketizationTimeMillis != 20 {
		t.Fatalf("media interop = %#v", cfg.Media)
	}
}

func TestRepositoryConfigurationsLoadAndValidate(t *testing.T) {
	for _, path := range []string{
		"../../gateway.example.yaml",
		"../../gateway.local.example.yaml",
		"../../deploy/spes/gateway.example.yaml",
		"../../deploy/huawei-ar6121e/gateway.example.yaml",
	} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load(%s): %v", path, err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate(%s): %v", path, err)
			}
		})
	}
}

func TestValidateRejectsInvalidMediaRange(t *testing.T) {
	cfg := Default()
	cfg.Media.PortMin = 4000
	cfg.Media.PortMax = 4000
	if err := cfg.Validate(); err == nil {
		t.Fatal("invalid media range was accepted")
	}
	cfg.Media.PortMin = 4001
	cfg.Media.PortMax = 4003
	if err := cfg.Validate(); err == nil {
		t.Fatal("media range with only one even RTP/RTCP pair was accepted")
	}
}

func TestValidateRejectsInvalidH248DTMFMode(t *testing.T) {
	cfg := Default()
	cfg.Media.H248DTMFMode = "sip-info"
	if err := cfg.Validate(); err == nil {
		t.Fatal("invalid H.248 DTMF mode was accepted")
	}
}
