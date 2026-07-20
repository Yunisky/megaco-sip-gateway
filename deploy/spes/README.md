[English](README.md) | [中文](README.zh-CN.md)

# Dual-homed SPES/openEuler deployment example

This is an anonymized example for a VM with separate enterprise SIP and
carrier H.248 networks. All checked-in addresses are RFC 5737 documentation
addresses. Interface names, routes, VRF table IDs, ports, and address ranges
must be reviewed for each deployment.

## Example layout

```text
enterprise interface -> main routing table -> SIP signaling and SIP RTP
carrier interface    -> vrf-h248          -> H.248 signaling and carrier RTP
```

The sample `gateway.example.yaml` uses:

- an enterprise-side documentation address from `192.0.2.0/24`;
- a carrier-side documentation address from `198.51.100.0/24`;
- `vrf-h248` as the Linux bind device;
- H.248 text over UDP and PCMA media;
- independent even/odd RTP/RTCP port pairs on both legs.

Copy and edit the profile before installation:

```sh
cp deploy/spes/gateway.example.yaml gateway.yaml
chmod 600 gateway.yaml
./h248-sip-gateway -config gateway.yaml -check-config
```

Never route RFC 5737 addresses in a real deployment. Replace all of them with
the addresses assigned to the gateway, PBX, and MGCs.

## VRF checklist

1. Create a dedicated VRF and routing table for the carrier interface.
2. Move only the carrier-facing interface into that VRF.
3. Install the carrier gateway and MGC routes in the VRF table.
4. Keep the management and PBX default route in the main table.
5. Set `h248.bind_device` to the VRF master.
6. Confirm `media.h248_rtp_ip` belongs to the carrier interface.
7. Confirm `media.rtp_ip` is reachable by the PBX through the main table.

Example commands with placeholders:

```sh
ip link add VRF_NAME type vrf table VRF_TABLE_ID
ip link set VRF_NAME up
ip link set CARRIER_IFACE master VRF_NAME
ip addr add CARRIER_IP/CARRIER_PREFIX dev CARRIER_IFACE
ip route add table VRF_TABLE_ID default via CARRIER_GATEWAY dev CARRIER_IFACE
ip vrf exec VRF_NAME ping -c 3 PRIMARY_MGC_IP
```

Persist the equivalent configuration with NetworkManager, systemd-networkd,
or the distribution's network manager. Do not apply remote VRF changes without
an out-of-band recovery path.

## Install and verify

Use the repository installer after editing the configuration:

```sh
sudo ./scripts/install.sh --config ./gateway.yaml
systemctl status h248-sip-gateway --no-pager
journalctl -u h248-sip-gateway -f
```

Expected listeners are the configured SIP address in the main table and the
H.248 address associated with the VRF bind device. Use `ss -lnup` and the
gateway logs to verify both without recording production addresses in tickets
or source control.

Firewall policy should allow only:

```text
PBX_ADDRESS_SET -> SIP listener and SIP-side RTP/RTCP range
MGC_ADDRESS_SET -> H.248 listener
CARRIER_MEDIA_SET -> carrier-side RTP/RTCP range
```

The permitted RTP range must include the odd RTCP port immediately after the
last even RTP port.

## Bounded probes

Stop the gateway before running a probe that binds the same H.248 source port:

```sh
systemctl stop h248-sip-gateway
h248-probe \
  -service-change \
  -target PRIMARY_MGC_IP:H248_PORT \
  -source GATEWAY_H248_IP:H248_PORT \
  -bind-device VRF_NAME \
  -version H248_VERSION \
  -mid COMPLETE_MGID \
  -count 3 \
  -timeout 3s
systemctl start h248-sip-gateway
```

Do not commit generated `gateway.yaml`, probe output, packet captures, logs,
network-manager profiles, or carrier configuration exports.
