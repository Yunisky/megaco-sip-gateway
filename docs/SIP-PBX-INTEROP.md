[English](SIP-PBX-INTEROP.md) | [中文](SIP-PBX-INTEROP.zh-CN.md)

# SIP PBX Interoperability Guide

This document explains how any SIP PBX can connect to
`h248-sip-gateway` through a registration-free, source-IP-authenticated SIP
trunk. The Huawei VRP, Asterisk/PJSIP, and FreePBX configurations are
anonymized, generic references and contain no production addresses, ports, or
numbers.

## 1. Interworking model

```text
Carrier inbound: H.248 MGC -> gateway -> SIP INVITE -> PBX inbound route -> extension/queue
Enterprise outbound: extension -> PBX outbound route -> SIP INVITE -> gateway -> H.248 MGC
```

The gateway does not register with the PBX. The two sides establish a SIP trunk
using fixed source addresses:

- the PBX defines the gateway IP:port as a registration-free SIP Trunk;
- the gateway points `sip.outbound_proxy` to the PBX trunk listener;
- the PBX accepts inbound SIP only from the gateway IP;
- the gateway host firewall accepts SIP/RTP only from the PBX IP.

## 2. SIP and media parameters

| Parameter | Recommended value |
|---|---|
| SIP transport | UDP |
| SIP trunk authentication | No registration; identify by source IP |
| Codec | PCMA/G.711A, payload 8 |
| Packetization | 20 ms |
| DTMF | RFC4733/RFC2833 telephone-event |
| SIP RTP | Real, routable addresses between the PBX and gateway |
| NAT | Prefer disabled; if present, all Via/Contact/SDP addresses must remain reachable |
| Early media | Permit 183 + SDP and in-band ringback |
| CANCEL | Return 200 correctly and return 487 for the original INVITE |
| Re-INVITE/UPDATE | Permit media updates; do not force an unsupported Codec |

When the carrier leg uses PCMA, the PBX must allow PCMA. Even if its endpoints
also offer Opus, G.722, or PCMU, retain PCMA in the offer, preferably first. The
gateway does not transcode speech Codecs.

## 3. Gateway SIP configuration

```yaml
sip:
  listen: "GATEWAY_SIP_IP:5060"
  bind_device: ""
  advertised_address: "GATEWAY_SIP_IP:5060"
  domain: "GATEWAY_SIP_IP"
  trunk_uri: "sip:INBOUND_NUMBER@PBX_DOMAIN"
  outbound_proxy: "PBX_IP:PBX_TRUNK_PORT"
  user_agent: "h248-sip-gateway"

media:
  # Keep rfc4733 when the carrier retains telephone-event in final H.248 SDP.
  # Use inband when the carrier accepts only PCMA tones for IVR input.
  h248_dtmf_mode: "rfc4733"
```

Field relationships:

- `listen` is the destination of PBX-originated INVITEs;
- `advertised_address` is the gateway address placed in Via and Contact;
- `trunk_uri` supplies the SIP Request-URI and initial To for a
  carrier-originated call;
- `outbound_proxy` is the PBX address to which that INVITE is actually sent;
- `media.h248_dtmf_mode` controls only the DTMF representation sent from the
  gateway toward the H.248 carrier leg. The PBX may continue to use
  RFC4733/RFC2833 in either mode. Use `inband` only when the final H.248 media
  descriptor does not accept `telephone-event`.

The Request-URI and actual destination can differ. For example, the PBX may
require `sip:6001@pbx.example` in the URI while its registration-free trunk
listens at `PBX_IP:5061`.

## 4. Generic PBX configuration procedure

1. Create a UDP SIP trunk with no registration, identified by source IP.
2. Set the trunk peer to the gateway's `sip.listen` address.
3. Enable only PCMA/G.711A, or at least place PCMA first in the preference list.
4. Select RFC4733/RFC2833 for DTMF.
5. Create an inbound route that sends the number in `sip.trunk_uri` to the
   destination extension, ring group, queue, or IVR.
6. Create an outbound route that sends only allowed number patterns to this
   trunk, without adding an unagreed access prefix.
7. Confirm that the PBX internal dial plan actually includes the destination
   extension range.
8. Restrict the trunk to the gateway source IP, permitted number classes, and
   an appropriate concurrency limit.
9. Ensure the PBX SDP advertises an RTP address reachable by the gateway, not a
   management address or an unreachable private address.

## 5. Huawei VRP configuration outline

The following is an anonymized command outline. The command tree differs by
model, VRP release, and license. Verify each command with context-sensitive
device help before committing it:

```text
voice
 voip-address media interface PBX_VLANIF PBX_IP
 voip-address signalling interface PBX_VLANIF PBX_IP

 sipserver
  signalling-address ip PBX_IP port SIP_REGISTRAR_PORT
  media-ip PBX_IP
  register-uri PBX_IP
  home-domain PBX_IP

 callsource H248GW-IN

 gnr-number INBOUND_EXTENSION
  full-number INBOUND_EXTENSION

 callroute H248GW
  selecttype callertimebase

 trunk-group H248GW sip no-register
  description H248-SIP-Gateway
  default-caller-telno DEFAULT_CALLER
  callroute H248GW select-level 1
  callsource H248GW-IN
  signalling-address ip PBX_IP port PBX_TRUNK_PORT
  media-ip PBX_IP
  peer-address static GATEWAY_IP GATEWAY_SIP_PORT
  home-domain PBX_IP
  gnr-number INBOUND_EXTENSION

 callprefix INTERNAL
  prefix INTERNAL_PREFIX
  call-type category basic-service attribute 0
  digit-length INTERNAL_MIN_LENGTH INTERNAL_MAX_LENGTH
```

Key points:

- the existence and registration of a Huawei `pbxuser` does not automatically
  add its number to the dial plan;
- a matching `callprefix INTERNAL` is required for the internal extension
  range;
- `attribute 0` means `Internal dialing`; do not confuse it with `attribute 1
  Local dialing`;
- without the internal callprefix, Huawei returns `404 Not Found` and
  `Q.850 cause=1 Unallocated number`;
- the trunk Enterprise and Dn-set must be consistent with the PBX user;
- `Callee number analysis failed` in `display voice trace` is the most direct
  diagnostic evidence.

Split outbound number analysis into narrowly scoped `callprefix` entries based
on enterprise location and the carrier work order, and route all permitted
entries to `H248GW`. Do not publish live customer-service, mobile, toll, or
local-number prefixes in the public repository. If an internal extension prefix
overlaps an outside-line prefix, define longer, more specific outside routes
and test both the internal short number and the outside long number. Do not add
or remove an access code implicitly unless the enterprise dialing policy
explicitly requires it.

The anonymized Huawei-side outline is also documented in
[`deploy/huawei-ar6121e/README.md`](../deploy/huawei-ar6121e/README.md).

## 6. Asterisk/PJSIP example

This example identifies the trunk by the gateway source IP. Replace every
uppercase placeholder.

`pjsip.conf`:

```ini
[transport-udp]
type=transport
protocol=udp
bind=PBX_IP:5060

[h248gw]
type=endpoint
transport=transport-udp
context=from-h248gw
disallow=all
allow=alaw
direct_media=no
aors=h248gw

[h248gw]
type=aor
contact=sip:GATEWAY_IP:5060
qualify_frequency=30

[h248gw-identify]
type=identify
endpoint=h248gw
match=GATEWAY_IP/32
```

If the configuration parser does not allow an endpoint and aor to have the
same name, rename the aor to `h248gw-aor` and update the endpoint's `aors=`
setting accordingly.

`extensions.conf`:

```ini
[from-h248gw]
exten => INBOUND_EXTENSION,1,NoOp(H.248 carrier incoming call)
 same => n,Dial(PJSIP/DESTINATION_EXTENSION,30)
 same => n,Hangup()

[internal-outbound]
exten => _9X.,1,NoOp(H.248 carrier outbound call)
 same => n,Dial(PJSIP/${EXTEN:1}@h248gw,60)
 same => n,Hangup()
```

Corresponding gateway configuration:

```yaml
sip:
  trunk_uri: "sip:INBOUND_EXTENSION@PBX_IP"
  outbound_proxy: "PBX_IP:PBX_TRUNK_PORT"
```

## 7. FreePBX integration

Create a PJSIP Trunk in FreePBX:

- Authentication: None;
- Registration: None;
- SIP Server: gateway IP;
- SIP Server Port: the gateway `sip.listen` port;
- Match (Permit): gateway IP/32;
- Codecs: enable only alaw, or place alaw first;
- DTMF Mode: RFC4733;
- Direct Media: disabled;
- From Domain/Contact User: follow the local number plan, and do not transform
  numbers into strings the gateway cannot parse.

Then create:

- an Inbound Route whose DID matches the user part of the gateway `trunk_uri`,
  with an extension, queue, or other destination;
- an Outbound Route for the permitted mobile or E.164 patterns, selecting the
  H248GW trunk.

## 8. Call acceptance tests

| Test | Expected result |
|---|---|
| PBX outbound | 100/183/200/ACK and bidirectional PCMA RTP |
| PBX-side release | SIP BYE, H.248 `al/on`, and resource release |
| Carrier-side release | H.248 release and gateway-originated BYE to the PBX |
| Carrier inbound | 100/180/200, extension ringing, answer, and two-way audio |
| Carrier release while ringing | CANCEL 200, INVITE 487, and ACK |
| Immediate inbound call after release | New Context, normal 180, and no busy response |
| DTMF | With `rfc4733`, correct payload negotiation/rewriting; with `inband`, only PCMA dual tones on the carrier leg and a responsive IVR |

## 9. Troubleshooting

### 404 / Q.850 cause 1

The PBX received the trunk INVITE, but the called number is absent from its dial
plan. Check:

- the Request-URI and To user parts;
- the PBX inbound DID;
- the internal extension prefix;
- Enterprise/Dn-set/tenant;
- trunk callsource or inbound number mapping.

### 486 or persistent busy after release

Confirm that the preceding call completed:

- SIP BYE or CANCEL;
- CANCEL 200 and INVITE 487/ACK;
- H.248 `al/on` Reply;
- Context, logical trunk circuit, and RTP ports returned to Idle;
- the PBX is not retransmitting an old INVITE.

### One-way or no audio

Check both SDP bodies:

- each `c=` address is reachable from the peer network;
- RTP ports are inside the firewall allowlist;
- the PBX selected PCMA/PT8;
- the PBX is not passing an endpoint's unreachable address directly to the
  gateway;
- packet counters in both RTP directions increase in the gateway logs.

### Inbound works, but outbound makes no progress

Check the carrier DigitMap, off-hook stabilization window, called-number format,
and the H.248 `dd/ce` digit-completion event. Do not originate the first call in
the same millisecond in which gateway ServiceChange completes.

### Audio works, but the IVR does not respond to keys

Troubleshoot the actual media negotiation, not only the DTMF setting displayed
by the PBX interface:

1. Check the `telephone-event` payload and 8000 Hz clock in the SIP
   offer/answer.
2. Capture PBX-to-gateway RTP and confirm each key produces a short RFC4733
   event with a valid event code, duration, and End bit.
3. Inspect the Local/Remote Descriptor in the MGC's final H.248 Modify, not only
   the initial Add. Confirm whether the final descriptor still includes
   `telephone-event`.
4. In `rfc4733` mode, confirm that gateway-to-carrier event packets use the
   payload ultimately accepted by the MGC.
5. If complete RFC4733 packets arrive but the IVR still ignores them, or if the
   final H.248 SDP retains only PCMA/PT8, change `media.h248_dtmf_mode` to
   `inband`, validate the configuration, and restart the gateway.

In `inband` mode, the SIP PBX continues to send RFC4733. The gateway synthesizes
events 0-15 as PCMA dual tones, and its H.248 Add Reply no longer advertises
`telephone-event`. A carrier-side capture should show only normal 172-byte
PCMA RTP packets (12-byte RTP header plus 160-byte G.711A payload), with no
16-byte telephone-event RTP packets. This mode was validated against an
anonymized carrier IVR.

## 10. Security recommendations

- restrict SIP by source IP on both the PBX and gateway;
- allow only the outbound number patterns required by the business;
- limit trunk concurrency;
- allow only root and the service account to read configuration and log
  directories;
- never commit SIP extension passwords, router configurations, carrier work
  orders, or PCAP files to Git;
- for public or otherwise untrusted networks, deploy only after adding SIP
  TLS/SRTP support or an outer VPN.
