[English](DEPLOYMENT.md) | [中文](DEPLOYMENT.zh-CN.md)

# H.248 ↔ SIP Gateway Deployment Guide

This guide covers deploying one H.248 outside line from a GitHub Release in a
dedicated Linux VM or a host-networked container. For production, systemd on a
dedicated VM is recommended, with one gateway instance per line.

## 1. Parameters to confirm before deployment

| Category | Required information |
|---|---|
| Enterprise side | Gateway SIP IP, PBX IP/port, inbound destination number, and SIP-side RTP address and port pool |
| Carrier side | Local H.248 IP/prefix/gateway, VRF/interface, primary and backup MGCs, MGID, and Termination |
| Protocol | H.248 version/transport/encoding, ServiceChange parameters, Codec, DTMF payload, and ptime |
| Security | PBX and MGC source-address allowlists and SIP/H.248/RTP firewall ranges |

The current stable release requires:

- Linux with systemd;
- H.248 v1-v3 text over UDP; live-line validation was performed with v1;
- SIP over UDP;
- PCMA/G.711A support on both the PBX and carrier sides;
- a service account with `CAP_NET_RAW` so `SO_BINDTODEVICE` can be used;
- H.248 and SIP addresses, including the addresses advertised in SDP, that are
  actually reachable from their respective networks.

## 2. Recommended network topology

```text
Enterprise network / main routing table
  GATEWAY_SIP_IP
  SIP_PORT/UDP + SIP-side RTP
          |
      gateway VM
          |
  vrf-h248 / carrier private network
  H248_PORT/UDP + carrier-side RTP
```

Place the carrier interface in a separate VRF so the carrier's private
addresses, default route, or overlapping prefixes cannot affect the management
network. The following is a one-time `iproute2` example. Replace the interface,
addresses, and table number before use:

```sh
ip link add vrf-h248 type vrf table 248
ip link set vrf-h248 up
ip link set CARRIER_IFACE master vrf-h248
ip addr add GATEWAY_H248_IP/CARRIER_PREFIX dev CARRIER_IFACE
ip link set CARRIER_IFACE up
ip route add table 248 default via CARRIER_GATEWAY dev CARRIER_IFACE
ip vrf exec vrf-h248 ping -c 3 PRIMARY_MGC_IP
```

These commands do not persist across a reboot. Use NetworkManager,
systemd-networkd, or the distribution's network configuration system to make
them persistent. Before modifying VRF state on a remote server, retain
out-of-band management or a tested rollback window.

## 3. Download and verify a Release

For `${VERSION}` on Linux amd64:

```sh
VERSION=vX.Y.Z
curl -fLO "https://github.com/Yunisky/megaco-sip-gateway/releases/download/${VERSION}/h248-sip-gateway-${VERSION}-linux-amd64.tar.gz"
curl -fLO "https://github.com/Yunisky/megaco-sip-gateway/releases/download/${VERSION}/checksums.txt"
sha256sum -c checksums.txt --ignore-missing
tar -xzf "h248-sip-gateway-${VERSION}-linux-amd64.tar.gz"
cd "h248-sip-gateway-${VERSION}-linux-amd64"
```

On an arm64 host, replace `amd64` with `arm64` in the filename. Alternatively,
download only `install.sh`; it downloads the Release binary and systemd unit
for the host architecture and verifies the binary against the Release
`checksums.txt`.

## 4. Prepare gateway.yaml

Copy the template:

```sh
cp gateway.example.yaml gateway.yaml
chmod 600 gateway.yaml
vi gateway.yaml
```

The most important production fields are:

| Field | Meaning |
|---|---|
| `sip.listen` | Enterprise-side IP:port on which the gateway listens |
| `sip.advertised_address` | Reachable IP:port advertised in SIP Via/Contact |
| `sip.domain` | Gateway SIP domain or address |
| `sip.trunk_uri` | Request-URI sent to the PBX for carrier-originated calls |
| `sip.outbound_proxy` | PBX IP:port to which SIP messages are actually sent |
| `h248.listen` | Local MG IP:port assigned by the carrier |
| `h248.bind_device` | Carrier interface or VRF master, such as `vrf-h248` |
| `h248.mg_id` | Complete on-wire MID; it may be `[IP]:port` and need not equal the logical ID shown on the carrier work order |
| `h248.mgc` / `backup_mgc` | Primary and backup MGC IP:port |
| `h248.physical_termination` | Physical line, such as `A0` |
| `media.rtp_ip` | RTP address advertised toward the SIP/PBX side |
| `media.h248_rtp_ip` | RTP address advertised toward the carrier H.248 side |
| `media.port_min/max` | Even RTP ports allocated by the gateway; RTCP uses the next odd port |
| `media.h248_dtmf_mode` | `rfc4733` for dynamic event negotiation/rewriting, or `inband` to synthesize SIP RFC4733 as PCMA dual tones on the H.248 leg |

See these anonymized examples:

- `deploy/spes/gateway.example.yaml` for dual-interface/VRF H.248 parameters;
- `deploy/huawei-ar6121e/gateway.example.yaml` for Huawei PBX interworking.

RFC 5737 addresses in the examples cannot be used in production. Set the MGID,
MGCs, PBX, VRF, and media addresses for each line according to its work order.

## 5. One-command installation

First installation:

```sh
sudo ./install.sh --version "${VERSION}" --config ./gateway.yaml
```

When run from an extracted offline bundle, the script automatically uses the
bundled binary and unit without accessing the network:

```sh
sudo ./install.sh --config ./gateway.yaml
```

The script performs these steps in order:

1. Verifies Linux, systemd, and an amd64 or arm64 architecture.
2. Selects a bundled binary or downloads the requested Release.
3. Verifies its SHA-256 digest.
4. Runs `-check-config` with the candidate binary.
5. Creates the non-login `h248gw` system account.
6. Backs up the current binary, configuration, and unit to a timestamped,
   root-only directory.
7. Installs the binary, the configuration with mode 0640, and the systemd unit.
8. Validates the installed configuration again.
9. Runs daemon-reload, enable, and restart, then confirms the service is active.

The script does not change interfaces, VRFs, routes, DNS, or firewall rules. To
install without starting the service:

```sh
sudo ./install.sh --config ./gateway.yaml --no-start
```

## 6. Upgrade and rollback

Upgrade to a specified Release while retaining the current configuration:

```sh
sudo ./install.sh --version "${VERSION}"
```

Replace the configuration while upgrading:

```sh
sudo ./install.sh --version "${VERSION}" --config ./gateway.yaml
```

Each installation backup is stored under:

```text
/var/lib/h248-sip-gateway/backups/YYYYMMDDTHHMMSSZ/
```

Example manual rollback:

```sh
systemctl stop h248-sip-gateway
install -m 0755 BACKUP/h248-sip-gateway /usr/local/bin/h248-sip-gateway
install -o root -g h248gw -m 0640 BACKUP/gateway.yaml /etc/h248-sip-gateway/gateway.yaml
install -m 0644 BACKUP/h248-sip-gateway.service /etc/systemd/system/h248-sip-gateway.service
systemctl daemon-reload
systemctl restart h248-sip-gateway
```

## 7. Firewall

Apply least-privilege rules:

- on the enterprise side, allow only the PBX IP to reach the gateway SIP UDP
  port;
- on the enterprise side, allow only the PBX IP to reach the gateway RTP/RTCP
  port pool;
- in the H.248 VRF, allow only the primary and backup MGCs to reach the H.248
  UDP port;
- in the H.248 VRF, allow carrier media addresses to reach the RTP/RTCP port
  pool;
- do not expose SIP 5060 or a broad RTP range directly to untrusted networks.

The firewall range must extend from `media.port_min` through
`media.port_max + 1`, because the final odd port is used for RTCP. Do not copy
another deployment's port pool without review.

## 8. Post-deployment checks

```sh
/usr/local/bin/h248-sip-gateway -version
/usr/local/bin/h248-sip-gateway \
  -config /etc/h248-sip-gateway/gateway.yaml \
  -check-config
systemctl is-enabled h248-sip-gateway
systemctl is-active h248-sip-gateway
journalctl -u h248-sip-gateway -n 100 --no-pager
ss -lunp | grep -E ':(2944|5060)'
```

Startup logs must show:

- a ROOT ServiceChange Reply;
- a physical-Termination ServiceChange Reply;
- `h248 registration active`;
- an initial `al/on` Reply;
- no H.248 error descriptor.

## 9. Production acceptance matrix

At minimum, test:

1. PBX extension outbound call, answer, and two-way audio.
2. PBX-side release.
3. Carrier-side release.
4. Carrier-originated call, PBX ringing, answer, and two-way audio.
5. Carrier release while ringing, confirming `CANCEL/200/487/ACK`.
6. Another inbound call two to five seconds after release, confirming the line
   is not busy.
7. Backup-MGC operation when the primary is unreachable, only during a
   maintenance window.
8. Automatic ServiceChange and line recovery after a gateway restart.

Both directional RTP counters in the logs must be greater than zero. At the end
of testing, the PBX user, H.248 Context, logical trunk circuit, and RTP ports
must all return to Idle/free state.

## 10. Docker

The container must use host networking, must be able to see the host VRF, and
requires `CAP_NET_RAW`. One container per outside line is recommended:

```yaml
services:
  gateway:
    image: h248-sip-gateway:${VERSION}
    network_mode: host
    cap_add: [NET_RAW]
    volumes:
      - ./gateway.yaml:/app/gateway.yaml:ro
    restart: unless-stopped
```

Use a dedicated VM if the container platform cannot provide host networking,
VRF visibility, and `SO_BINDTODEVICE`.

## 11. Troubleshooting

- No ServiceChange Reply: check the VRF route, source IP, MGC IP/port, MID, and
  carrier ACL.
- PBX returns 404/Q.850 cause 1: check the PBX inbound number, dial plan,
  Enterprise/DN-set, and internal-number callprefix.
- One-way or no audio: check both SDP `c=` addresses, RTP firewall rules, VRF
  binding, and bidirectional RTP counters.
- Line remains busy after release: check the `al/on` Reply, BYE/CANCEL, 487 ACK,
  Context release, and INVITE transaction expiration.
- Configuration is valid but the service does not start: check that the local
  IP is configured, the port is free, and the systemd unit has `CAP_NET_RAW`.
