package config

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	SIP   SIPConfig
	H248  H248Config
	Media MediaConfig
}

type SIPConfig struct {
	Listen            string
	BindDevice        string
	AdvertisedAddress string
	Domain            string
	TrunkURI          string
	OutboundProxy     string
	UserAgent         string
}

type H248Config struct {
	Listen                          string
	BindDevice                      string
	Transport                       string
	Encoding                        string
	Version                         int
	MGID                            string
	MGC                             string
	BackupMGC                       string
	ServiceChangeMethod             string
	ServiceChangeReason             string
	ServiceChangeProfile            string
	ServiceChangeAddress            string
	ServiceChangeRetrySeconds       int
	ServiceChangeMaxAttempts        int
	MGCFailureTimeoutSeconds        int
	OriginationStabilizationSeconds int
	DigitReportDelayMilliseconds    int
	PhysicalTermination             string
	EphemeralTerminationPrefix      string
}

type MediaConfig struct {
	RTPIP                   string
	H248RTPIP               string
	PortMin                 int
	PortMax                 int
	DTMFPayloadType         int
	H248DTMFPayloadType     int
	H248DTMFMode            string
	PacketizationTimeMillis int
	CodecList               []string
}

func Default() Config {
	return Config{
		SIP: SIPConfig{
			Listen:    "0.0.0.0:5060",
			Domain:    "h248-sip-gateway.local",
			TrunkURI:  "sip:h248-line@ippbx.local",
			UserAgent: "h248-sip-gateway",
		},
		H248: H248Config{
			Listen:                          "0.0.0.0:2944",
			Transport:                       "udp",
			Encoding:                        "text",
			Version:                         1,
			MGID:                            "mgw-1",
			MGC:                             "",
			ServiceChangeMethod:             "Restart",
			ServiceChangeReason:             "901 Cold Boot",
			ServiceChangeRetrySeconds:       5,
			ServiceChangeMaxAttempts:        3,
			MGCFailureTimeoutSeconds:        360,
			OriginationStabilizationSeconds: 5,
			DigitReportDelayMilliseconds:    200,
			PhysicalTermination:             "aaln/1",
			EphemeralTerminationPrefix:      "RTP/",
		},
		Media: MediaConfig{
			RTPIP:                   "127.0.0.1",
			H248RTPIP:               "127.0.0.1",
			PortMin:                 20000,
			PortMax:                 29998,
			DTMFPayloadType:         101,
			H248DTMFPayloadType:     102,
			H248DTMFMode:            "rfc4733",
			PacketizationTimeMillis: 20,
			CodecList:               []string{"PCMA/8000", "PCMU/8000"},
		},
	}
}

// Load reads a small YAML-like config file. It intentionally supports only
// key: value pairs and one-level sections so the gateway builds without
// external dependencies in restricted telecom environments.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	defer f.Close()

	section := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			section = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}

		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			return cfg, fmt.Errorf("invalid config line %q", line)
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"`)
		if err := apply(&cfg, section, strings.TrimSpace(key), value); err != nil {
			return cfg, err
		}
	}
	return cfg, scanner.Err()
}

func apply(cfg *Config, section, key, value string) error {
	switch section + "." + key {
	case "sip.listen":
		cfg.SIP.Listen = value
	case "sip.bind_device":
		cfg.SIP.BindDevice = value
	case "sip.advertised_address":
		cfg.SIP.AdvertisedAddress = value
	case "sip.domain":
		cfg.SIP.Domain = value
	case "sip.trunk_uri":
		cfg.SIP.TrunkURI = value
	case "sip.outbound_proxy":
		cfg.SIP.OutboundProxy = value
	case "sip.user_agent":
		cfg.SIP.UserAgent = value
	case "h248.listen":
		cfg.H248.Listen = value
	case "h248.bind_device":
		cfg.H248.BindDevice = value
	case "h248.transport":
		cfg.H248.Transport = strings.ToLower(value)
	case "h248.encoding":
		cfg.H248.Encoding = strings.ToLower(value)
	case "h248.version":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("h248.version: %w", err)
		}
		cfg.H248.Version = n
	case "h248.mg_id":
		cfg.H248.MGID = value
	case "h248.mgc":
		cfg.H248.MGC = value
	case "h248.backup_mgc":
		cfg.H248.BackupMGC = value
	case "h248.service_change_method":
		cfg.H248.ServiceChangeMethod = value
	case "h248.service_change_reason":
		cfg.H248.ServiceChangeReason = value
	case "h248.service_change_profile":
		cfg.H248.ServiceChangeProfile = value
	case "h248.service_change_address":
		cfg.H248.ServiceChangeAddress = value
	case "h248.service_change_retry_seconds":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("h248.service_change_retry_seconds: %w", err)
		}
		cfg.H248.ServiceChangeRetrySeconds = n
	case "h248.service_change_max_attempts":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("h248.service_change_max_attempts: %w", err)
		}
		cfg.H248.ServiceChangeMaxAttempts = n
	case "h248.mgc_failure_timeout_seconds":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("h248.mgc_failure_timeout_seconds: %w", err)
		}
		cfg.H248.MGCFailureTimeoutSeconds = n
	case "h248.origination_stabilization_seconds":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("h248.origination_stabilization_seconds: %w", err)
		}
		cfg.H248.OriginationStabilizationSeconds = n
	case "h248.digit_report_delay_milliseconds":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("h248.digit_report_delay_milliseconds: %w", err)
		}
		cfg.H248.DigitReportDelayMilliseconds = n
	case "h248.physical_termination":
		cfg.H248.PhysicalTermination = value
	case "h248.ephemeral_termination_prefix":
		cfg.H248.EphemeralTerminationPrefix = value
	case "media.rtp_ip":
		cfg.Media.RTPIP = value
	case "media.h248_rtp_ip":
		cfg.Media.H248RTPIP = value
	case "media.port_min":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("media.port_min: %w", err)
		}
		cfg.Media.PortMin = n
	case "media.port_max":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("media.port_max: %w", err)
		}
		cfg.Media.PortMax = n
	case "media.dtmf_payload_type":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("media.dtmf_payload_type: %w", err)
		}
		cfg.Media.DTMFPayloadType = n
	case "media.h248_dtmf_payload_type":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("media.h248_dtmf_payload_type: %w", err)
		}
		cfg.Media.H248DTMFPayloadType = n
	case "media.h248_dtmf_mode":
		cfg.Media.H248DTMFMode = strings.ToLower(value)
	case "media.ptime_ms":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("media.ptime_ms: %w", err)
		}
		cfg.Media.PacketizationTimeMillis = n
	case "media.codecs":
		cfg.Media.CodecList = splitList(value)
	default:
		return fmt.Errorf("unknown config key %s.%s", section, key)
	}
	return nil
}

func splitList(value string) []string {
	value = strings.Trim(value, "[]")
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(strings.TrimSpace(part), `"`)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (cfg Config) Validate() error {
	for name, address := range map[string]string{
		"sip.listen":  cfg.SIP.Listen,
		"h248.listen": cfg.H248.Listen,
	} {
		if err := validateHostPort(address); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	for name, address := range map[string]string{
		"h248.mgc":        cfg.H248.MGC,
		"h248.backup_mgc": cfg.H248.BackupMGC,
	} {
		if address != "" {
			if err := validateHostPort(address); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	if cfg.SIP.AdvertisedAddress != "" {
		if err := validateHostPort(cfg.SIP.AdvertisedAddress); err != nil {
			return fmt.Errorf("sip.advertised_address: %w", err)
		}
	}
	if cfg.SIP.OutboundProxy != "" {
		if err := validateHostPort(cfg.SIP.OutboundProxy); err != nil {
			return fmt.Errorf("sip.outbound_proxy: %w", err)
		}
	}
	if !strings.HasPrefix(strings.ToLower(cfg.SIP.TrunkURI), "sip:") {
		return fmt.Errorf("sip.trunk_uri must start with sip:")
	}
	if cfg.H248.Transport != "udp" {
		return fmt.Errorf("h248.transport %q is not implemented; use udp", cfg.H248.Transport)
	}
	if cfg.H248.Encoding != "text" {
		return fmt.Errorf("h248.encoding %q is not implemented; use text", cfg.H248.Encoding)
	}
	if cfg.H248.Version < 1 || cfg.H248.Version > 3 {
		return fmt.Errorf("h248.version must be between 1 and 3")
	}
	if cfg.H248.MGID == "" {
		return fmt.Errorf("h248.mg_id must not be empty")
	}
	if cfg.H248.MGC != "" && cfg.H248.PhysicalTermination == "" {
		return fmt.Errorf("h248.physical_termination must not be empty when h248.mgc is configured")
	}
	if cfg.H248.ServiceChangeRetrySeconds <= 0 || cfg.H248.ServiceChangeMaxAttempts <= 0 {
		return fmt.Errorf("H.248 ServiceChange retry and attempt values must be positive")
	}
	if cfg.H248.MGCFailureTimeoutSeconds < 1 {
		return fmt.Errorf("h248.mgc_failure_timeout_seconds must be positive")
	}
	if cfg.H248.OriginationStabilizationSeconds < 0 || cfg.H248.OriginationStabilizationSeconds > 300 {
		return fmt.Errorf("h248.origination_stabilization_seconds must be between 0 and 300")
	}
	if cfg.H248.DigitReportDelayMilliseconds < 0 || cfg.H248.DigitReportDelayMilliseconds > 10000 {
		return fmt.Errorf("h248.digit_report_delay_milliseconds must be between 0 and 10000")
	}
	if net.ParseIP(cfg.Media.RTPIP) == nil {
		return fmt.Errorf("media.rtp_ip %q is not an IP address", cfg.Media.RTPIP)
	}
	if net.ParseIP(cfg.Media.H248RTPIP) == nil {
		return fmt.Errorf("media.h248_rtp_ip %q is not an IP address", cfg.Media.H248RTPIP)
	}
	firstRTPPort := cfg.Media.PortMin
	if firstRTPPort%2 != 0 {
		firstRTPPort++
	}
	if cfg.Media.PortMin <= 0 || cfg.Media.PortMax > 65534 || firstRTPPort+2 > cfg.Media.PortMax {
		return fmt.Errorf("media port range must contain at least two even RTP ports with following RTCP ports and end at or below 65534")
	}
	if cfg.Media.DTMFPayloadType <= 0 || cfg.Media.DTMFPayloadType >= 128 || cfg.Media.H248DTMFPayloadType <= 0 || cfg.Media.H248DTMFPayloadType >= 128 {
		return fmt.Errorf("telephone-event payload types must be between 1 and 127")
	}
	if cfg.Media.H248DTMFMode != "rfc4733" && cfg.Media.H248DTMFMode != "inband" {
		return fmt.Errorf("media.h248_dtmf_mode must be rfc4733 or inband")
	}
	if cfg.Media.PacketizationTimeMillis <= 0 || cfg.Media.PacketizationTimeMillis > 200 {
		return fmt.Errorf("media.ptime_ms must be between 1 and 200")
	}
	if len(cfg.Media.CodecList) == 0 {
		return fmt.Errorf("media.codecs must not be empty")
	}
	return nil
}

func validateHostPort(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("host is empty")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("invalid port %q", portText)
	}
	return nil
}
