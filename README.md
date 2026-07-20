# H.248 ↔ SIP Gateway

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

`h248-sip-gateway` is an MG-side H.248/Megaco to SIP interworking gateway for
connecting a SIP IP-PBX to a carrier access line controlled through H.248.
Each outside line can run in its own VM or host-networked container so carrier
routing, VRF state, call state, and failures remain isolated.

The implementation has been exercised against a live carrier and a hardware
SIP PBX. All public examples are anonymized: routable-looking addresses belong
to the RFC 5737 documentation ranges and must be replaced before deployment.

## Documentation

- [`docs/H248-SIP-INTERWORKING.md`](docs/H248-SIP-INTERWORKING.md): signaling,
  call-state, media, release, and DTMF mapping;
- [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md): VM/container networking,
  systemd installation, rollback, firewalling, and acceptance tests;
- [`docs/SIP-PBX-INTEROP.md`](docs/SIP-PBX-INTEROP.md): generic SIP trunk,
  Huawei VRP, Asterisk/PJSIP, and FreePBX integration;
- [`deploy/spes/README.md`](deploy/spes/README.md): anonymized dual-homed
  Linux/VRF deployment example;
- [`deploy/huawei-ar6121e/README.md`](deploy/huawei-ar6121e/README.md):
  anonymized Huawei AR6121E interop outline;
- [`deploy/asterisk-lab/README.md`](deploy/asterisk-lab/README.md): reproducible
  Asterisk lab PBX.

## Implemented behavior

- H.248 v1–v3 text over UDP; live interoperability was validated with v1.
- Nested Transaction → Context → Command parsing, compact and long command
  names, multiple Contexts per transaction, and optional commands.
- MG-side ROOT and physical-termination `ServiceChange/Restart`.
- Ordered primary/backup MGC registration, bounded retry, activity timeout,
  active-call teardown, and failover.
- Exact H.248 reply caching and duplicate suppression.
- `ServiceChange`, `Add`, `Modify`, `Subtract`, `Notify`, `AuditValue`, and
  `AuditCapabilities` handling.
- Physical and ephemeral termination mapping, `andisp/dwa` caller-ID decoding,
  and configurable carrier DigitMap completion through `dd/ce`.
- H.248 Context ↔ SIP Dialog state mapping in both call directions.
- SIP/UDP INVITE, provisional/final responses, ACK, PRACK, CANCEL, BYE,
  re-INVITE, UPDATE, OPTIONS, and INFO.
- SIP server-transaction response caching and unanswered INVITE retransmission.
- Correct CANCEL 200, INVITE 487, and non-2xx ACK behavior.
- RTP/RTCP anchoring across the enterprise routing table and carrier VRF.
- Independent even/odd RTP/RTCP port pairs, symmetric peer learning, PCMA/PT8
  forwarding, and RTCP forwarding.
- RFC4733/RFC2833 telephone-event payload negotiation and rewriting.
- Optional RFC4733-to-PCMA in-band DTMF synthesis for carrier IVRs that remove
  or ignore `telephone-event` in the final H.248 media descriptor.
- Linux `SO_BINDTODEVICE` support for H.248 signaling and carrier-side media.
- Bounded H.248 and SIP probes, an authenticated SIP REGISTER probe, systemd
  deployment, a host-network container image, and an Asterisk lab PBX.

## Call mapping

Carrier-originated call:

```text
Gateway                 Carrier MGC                    IP-PBX
   | ROOT/line ServiceChange |                            |
   |------------------------>|                            |
   | initial on-hook Notify  |                            |
   |------------------------>|                            |
   |                         |                            |
   | Add physical + RTP term |                            |
   |<------------------------|                            |
   | Add Reply + local SDP   |                            |
   |------------------------>|                            |
   | ring/caller-ID events   |                            |
   |<------------------------|                            |
   |                         | SIP INVITE + SDP           |
   |                         |--------------------------->|
   |                         | SIP 180 / 200 + SDP        |
   |                         |<---------------------------|
   | answer event            |                            |
   |------------------------>|                            |
   |<============ anchored PCMA RTP/RTCP ================>|
```

PBX-originated call:

```text
IP-PBX                      Gateway                       Carrier MGC
  | SIP INVITE + SDP          |                                |
  |-------------------------->|                                |
  | 100 Trying                | off-hook + DigitMap request    |
  |<--------------------------|------------------------------->|
  |                           | completed digits (`dd/ce`)     |
  |                           |------------------------------->|
  |                           | Add physical + RTP termination |
  |                           |<------------------------------->|
  | 183 / 200 + SDP           | media activation               |
  |<--------------------------|<-------------------------------|
  | ACK                       |                                |
  |-------------------------->|                                |
  |<============ anchored PCMA RTP/RTCP ================>|
  | BYE                       | on-hook / Subtract              |
  |-------------------------->|------------------------------->|
```

Some FXS-style profiles provide call progress as in-band audio and no separate
remote-answer event. For those profiles, the gateway sends SIP 183 when the
H.248 Context is allocated and SIP 200 when the MGC activates send/receive
media.

## Build and test

Go 1.22 or newer is recommended:

```sh
go test ./...
go test -race ./...
go vet ./...
./scripts/check-public-tree.sh
go build ./cmd/gateway
go build ./cmd/h248-call-probe
go build ./cmd/sip-register-probe
```

The tests use loopback UDP sockets to exercise H.248 registration/failover,
SIP transactions, incoming and outgoing calls, RTP/RTCP forwarding, dynamic
telephone-event payload mapping, and RFC4733-to-PCMA DTMF synthesis. No carrier
or PBX is required for the automated suite.

## Configuration

Copy the public template to the ignored runtime file:

```sh
cp gateway.example.yaml gateway.yaml
go run ./cmd/gateway -config gateway.yaml -check-config
go run ./cmd/gateway -config gateway.yaml
```

Important settings:

- `sip.listen`: gateway listener on the PBX-facing network;
- `sip.advertised_address`: address placed in SIP Via and Contact;
- `sip.trunk_uri`: Request-URI generated for carrier-originated calls;
- `sip.outbound_proxy`: PBX SIP trunk listener;
- `h248.listen`: local carrier-side H.248 address;
- `h248.bind_device`: carrier interface or VRF master;
- `h248.mg_id`: complete wire mId assigned by the carrier;
- `h248.mgc` / `h248.backup_mgc`: ordered MGC endpoints;
- `h248.physical_termination`: carrier line termination;
- `media.rtp_ip`: PBX-facing RTP address;
- `media.h248_rtp_ip`: carrier-facing RTP address;
- `media.dtmf_payload_type`: SIP-side telephone-event payload;
- `media.h248_dtmf_payload_type`: fallback H.248 telephone-event payload;
- `media.h248_dtmf_mode`: `rfc4733` for negotiated event rewriting or
  `inband` for SIP event to H.248-side PCMA tone synthesis;
- `media.port_min` / `media.port_max`: even RTP allocation range. Permit the
  following odd RTCP port through `media.port_max + 1`.

On Linux, binding to a device or VRF requires root or `CAP_NET_RAW`. The
supplied systemd unit grants the capability to a dedicated service account.

## Probe tools

Use placeholders or addresses explicitly assigned to your lab. A bounded MGC
registration probe looks like this:

```sh
go run ./cmd/h248-probe \
  -service-change \
  -target MGC_IP:H248_PORT \
  -source MG_IP:H248_PORT \
  -bind-device CARRIER_VRF \
  -version H248_VERSION \
  -mid COMPLETE_MGID \
  -count 3 \
  -timeout 3s
```

The stateful call probe binds the same H.248 source address as the gateway, so
stop the gateway before using it:

```sh
go run ./cmd/h248-call-probe \
  -mgc MGC_IP:H248_PORT \
  -source MG_IP:H248_PORT \
  -bind-device CARRIER_VRF \
  -mid COMPLETE_MGID \
  -termination PHYSICAL_TERMINATION \
  -mode inbound-answer \
  -line-service-change \
  -rtp-ip MG_RTP_IP \
  -rtp-port MG_RTP_PORT \
  -timeout 180s
```

Validate a SIP registrar without placing the password in process arguments:

```sh
go run ./cmd/sip-register-probe \
  -target PBX_IP:SIP_PORT \
  -username TEST_EXTENSION \
  -password-file /path/to/extension.password \
  -hold 35s
```

Run a local SIP-originated call probe against a loopback gateway:

```sh
go run ./cmd/sip-probe \
  -target 127.0.0.1:5060 \
  -source 127.0.0.1:0 \
  -advertised-ip 127.0.0.1 \
  -rtp-source 127.0.0.1:40000 \
  -number TEST_DESTINATION \
  -hold 10s \
  -timeout 60s
```

Use only destinations and carrier resources explicitly authorized for testing.

## Docker

```sh
docker compose up --build
```

The image uses host networking because SIP and H.248 carry network addresses
inside protocol bodies and media may cross two routing domains. Prefer a VM if
the container platform cannot expose the host VRF or `SO_BINDTODEVICE`.

## Current boundaries

The current implementation does not provide H.248 ASN.1 binary encoding,
H.248 TCP/SCTP, SIP TCP/TLS, SRTP, T.38, SIP trunk Digest authentication, or
general-purpose media transcoding. PCMA speech is relayed without transcoding;
in-band DTMF mode only synthesizes keypad tones.

## Privacy and security

- Never commit `gateway.yaml`, credentials, subscriber identifiers, carrier
  work orders, exported CPE/router configurations, packet captures, logs, or
  rollback paths.
- Use RFC 5737 networks (`192.0.2.0/24`, `198.51.100.0/24`, and
  `203.0.113.0/24`) in documentation and test fixtures.
- Restrict SIP, H.248, RTP, and RTCP by peer address and minimum required port
  range.
- Run one line per isolated instance when practical.

## License

Licensed under the [Apache License 2.0](LICENSE).
