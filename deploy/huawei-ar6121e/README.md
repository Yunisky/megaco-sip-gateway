[English](README.md) | [中文](README.zh-CN.md)

# Huawei AR6121E SIP PBX integration

This directory contains an anonymized example for connecting a Huawei
AR6121E/VRP voice service to `h248-sip-gateway`. It contains no customer,
carrier, router, extension, or production-network identifiers.

All addresses in `gateway.example.yaml` belong to the RFC 5737 documentation
ranges. Replace them before deployment. The port values are standard or
illustrative service ports, not values copied from a live environment.

## Topology

```text
Carrier MGC
    |
    | H.248 text/UDP and RTP through a carrier-side VRF
    v
h248-sip-gateway
    |
    | source-IP authenticated SIP trunk and anchored RTP
    v
Huawei AR6121E / VRP voice service
    |
    v
SIP extensions, hunt groups, queues, or IVRs
```

The gateway does not register to the Huawei PBX. The Huawei trunk identifies
the gateway by its fixed source address. End-user SIP extensions continue to
register to the PBX normally.

## Gateway configuration

Copy the anonymized example and replace every documentation address:

```sh
cp deploy/huawei-ar6121e/gateway.example.yaml gateway.yaml
/usr/local/bin/h248-sip-gateway -config gateway.yaml -check-config
```

The signaling destination and called-number URI are intentionally independent:

- `sip.outbound_proxy` is the Huawei no-register trunk listener;
- `sip.trunk_uri` carries the inbound DID or extension that the Huawei called
  number analyzer must match;
- `sip.listen` is the address to which Huawei sends outbound trunk calls.

For carriers whose final H.248 Local/Remote Descriptor removes or ignores
`telephone-event`, use:

```yaml
media:
  h248_dtmf_mode: "inband"
```

The Huawei PBX can still send RFC4733. The gateway then advertises only
PCMA/PT8 on the H.248 leg and synthesizes G.711 A-law DTMF tones for the
carrier IVR. Use the default `rfc4733` mode when the carrier accepts negotiated
telephone-event RTP.

## Huawei voice configuration outline

The exact command tree varies by VRP release and license. The following is a
placeholder-only outline; replace every uppercase token and verify commands
with the device's context-sensitive help before committing the configuration:

```text
voice
 voip-address media interface PBX_VLANIF PBX_IP
 voip-address signalling interface PBX_VLANIF PBX_IP

 sipserver
  signalling-address ip PBX_IP port SIP_REGISTRAR_PORT
  media-ip PBX_IP
  register-uri PBX_IP
  home-domain PBX_IP

 callroute H248GW
  selecttype callertimebase

 callsource H248GW-IN

 gnr-number INBOUND_EXTENSION
  full-number INBOUND_EXTENSION

 trunk-group H248GW sip no-register
  description H248-SIP-Gateway
  default-caller-telno DEFAULT_CALLER
  callroute H248GW select-level 1
  callsource H248GW-IN
  signalling-address ip PBX_IP port PBX_TRUNK_PORT
  media-ip PBX_IP
  peer-address static GATEWAY_SIP_IP GATEWAY_SIP_PORT
  home-domain PBX_IP
  gnr-number INBOUND_EXTENSION

 callprefix INTERNAL
  prefix INTERNAL_PREFIX
  call-type category basic-service attribute 0
  digit-length INTERNAL_MIN_LENGTH INTERNAL_MAX_LENGTH
```

Important Huawei behaviors:

- A registered `pbxuser` does not automatically make its number dialable.
  Create a matching internal `callprefix`.
- VRP attribute `0` represents internal dialing; outbound carrier routes use
  the appropriate local or toll category for the installed release.
- A missing called-number route commonly returns SIP `404 Not Found` with
  Q.850 cause 1. `display voice trace` may report `Callee number analysis
  failed`.
- The trunk Enterprise, Dn-set, callsource, and extension objects must be
  mutually consistent.
- Save the running configuration only after inbound, outbound, release, and
  immediate-redial tests pass.

## Outbound number analysis

Create narrowly scoped `callprefix` groups and route each permitted class to
`H248GW`. A typical plan may include:

| Number class | Pattern | Example total length |
|---|---|---:|
| Customer/service numbers | configured service prefixes | carrier-specific |
| National service numbers | configured national prefix | carrier-specific |
| Mobile numbers | national mobile prefixes | 11 |
| Toll mobile numbers | trunk prefix plus mobile number | 12 |
| Local fixed-line numbers | non-zero leading digit | 8 |

Do not add or strip an access digit unless that transformation is explicitly
part of the enterprise dialing policy. Where an internal extension prefix
overlaps an external fixed-line prefix, split the external route into longer,
more specific prefixes so the internal route remains unambiguous.

## Firewall

Use address objects rather than embedding live addresses in documentation:

```text
PBX_IP/32 -> GATEWAY_SIP_IP:GATEWAY_SIP_PORT/udp
PBX_IP/32 -> GATEWAY_SIP_IP:GATEWAY_RTP_RANGE/udp
MGC_ADDRESS_SET -> GATEWAY_H248_IP:H248_PORT/udp
CARRIER_MEDIA_SET -> GATEWAY_H248_IP:H248_RTP_RANGE/udp
```

RTP allocators reserve an even RTP port and the following odd RTCP port. If
`media.port_max` is the last even RTP port, permit through
`media.port_max + 1`.

## Acceptance tests

Verify all of the following before production use:

1. PBX trunk status is normal and the target extension is registered.
2. PBX-originated calls reach the carrier and pass two-way PCMA audio.
3. Carrier-originated calls produce SIP 100/180/200 and two-way audio.
4. PBX and carrier release each tear down SIP, H.248 Context, RTP, and RTCP.
5. A second inbound call immediately after release rings instead of returning
   busy.
6. An unanswered call cancelled by the carrier completes SIP CANCEL/487/ACK.
7. DTMF works against an IVR. In `inband` mode, the carrier leg should contain
   PCMA tone packets and no telephone-event packets.

Never commit router credentials, extension passwords, exported configurations,
packet captures, production logs, or rollback file paths.
