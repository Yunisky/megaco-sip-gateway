[English](H248-SIP-INTERWORKING.md) | [中文](H248-SIP-INTERWORKING.zh-CN.md)

# H.248-to-SIP Interworking Implementation

This document summarizes the H.248/Megaco and SIP interworking implemented by
this project and validated against an anonymized live line. It describes the
gateway and protocol mapping; it does not replace the carrier's parameter sheet
for a specific line. No real MGC, MG, or PBX addresses or subscriber numbers
appear here.

## 1. Gateway roles in the two networks

```text
Carrier H.248 MGC                    Enterprise SIP PBX
        |                                 |
        | H.248 signaling                 | SIP signaling
        | Carrier RTP                     | Enterprise RTP
        v                                 v
             h248-sip-gateway
       MG on the H.248 side, B2BUA on the SIP side
```

On the carrier side, the gateway is a Media Gateway (MG) controlled by the
carrier's Media Gateway Controller (MGC). It manages `ServiceChange`, the
physical Termination, ephemeral RTP Terminations, Contexts, and line events.

On the enterprise side, the gateway is a registration-free SIP trunk endpoint.
It also anchors RTP/RTCP between the SIP and H.248 legs. The SIP PBX does not
need to understand H.248, and the carrier MGC does not need to understand SIP.

A dedicated VM or host-networked container is recommended for each carrier
outside line. This isolates:

- the H.248 MGID, physical Termination, and Context state;
- the carrier VRF, IP addressing, routing, and failure domain;
- the SIP trunk port and RTP port pools;
- upgrades, restarts, and rollback for an individual line.

## 2. Core object mapping

| H.248 object or event | SIP object or action |
|---|---|
| MG/MID | SIP gateway instance and Via/Contact address |
| Physical Termination, such as `A0` | One carrier outside line |
| Ephemeral Termination, such as `RTP/1` | Media leg of one SIP Dialog |
| Context | SIP Call-ID/Dialog and internal Call state |
| `Add`/`Modify` + SDP | INVITE/183/200/UPDATE/re-INVITE + SDP |
| `al/of` | Off-hook or answer supervision; drives answer state according to call direction |
| `al/on` | On-hook; mapped to BYE, CANCEL, or Context release |
| `andisp/dwa` | Calling number, mapped to SIP From/PAI |
| `dd/ce` + DigitMap | Completion of SIP called-number digit collection |
| H.248 Transaction Reply | SIP transaction progress or error release |

The gateway does not merely rewrite one text message into another. H.248 and
SIP use different transaction models, call models, and media-update ordering.
The gateway must therefore maintain independent state machines and associate
one H.248 Context with one SIP Dialog.

## 3. Startup and line registration

After a production instance starts, it performs these steps in order:

1. Sends ROOT `ServiceChange/Restart` to the primary MGC.
2. After ROOT succeeds, sends `ServiceChange/Restart` for the physical
   Termination.
3. Sends an initial `al/on` to synchronize the line to the idle, on-hook state.
4. Enters a stabilization window before accepting a SIP-originated outbound
   call.
5. Monitors MGC activity, releasing calls and failing over to the backup MGC
   when required.

One anonymized live-line environment exhibited these protocol characteristics:

| Parameter | Observed value |
|---|---|
| H.248 transport | UDP |
| H.248 encoding | Text |
| H.248 version | v1 |
| MGC | Carrier-provided primary and backup addresses, not published |
| MGID | Complete IP/port form, exact value not published |
| ServiceChange | `Restart`, `901 Cold Boot`, no Profile |
| Physical Termination | `A0` |
| Ephemeral Termination prefix | `RTP/` |
| Codec | PCMA/PT8, 20 ms |
| DTMF | The initial descriptor may include telephone-event; the final descriptor may retain only PCMA |

These values belong to a line configuration and must not be hard-coded. Each
gateway instance sets them through `gateway.yaml`.

## 4. Carrier-originated call to the SIP PBX

The primary inbound flow is:

```text
MGC                         Gateway                         SIP PBX
 | Add A0 + $ + remote SDP     |                               |
 |---------------------------->|                               |
 | Add Reply + RTP/n + SDP     |                               |
 |<----------------------------|                               |
 | andisp/dwa calling number   |                               |
 |---------------------------->| INVITE destination + PAI+SDP |
 |                             |------------------------------>|
 |                             | 100 / 180                     |
 |                             |<------------------------------|
 |                             | 200 OK + SDP                  |
 |                             |<------------------------------|
 | al/of answer event          | ACK                           |
 |<----------------------------|------------------------------>|
 |<=================== bidirectional PCMA/RFC4733 RTP =========>|
```

The SIP Request-URI comes from `sip.trunk_uri`, while the UDP datagram is sent
to `sip.outbound_proxy`. This separation is intentional: the PBX may require a
local domain in the URI while listening for the registration-free trunk on a
dedicated address or port.

If the PBX returns a final non-2xx response, the gateway sends the corresponding
ACK, signals on-hook toward H.248, and releases the Context. If the carrier
releases before SIP answer, the gateway sends CANCEL and completes the full
`200 CANCEL + 487 INVITE + ACK` exchange.

## 5. SIP PBX-originated call to the carrier

The SIP PBX sends the number as the Request-URI user part:

```text
SIP PBX                     Gateway                         MGC
 | INVITE number + SDP        |                              |
 |--------------------------->| 100 Trying                   |
 |<---------------------------|                              |
 |                            | al/of off-hook                |
 |                            |----------------------------->|
 |                            | DigitMap/digit instructions  |
 |                            |<-----------------------------|
 |                            | dd/ce completed digits       |
 |                            |----------------------------->|
 |                            | Add A0 + $ / Context         |
 |                            |<---------------------------->|
 | 183 + SDP                  |                              |
 |<---------------------------|                              |
 | 200 OK + SDP               |                              |
 |<---------------------------|                              |
 | ACK                        |                              |
 |--------------------------->|                              |
 |<================== bidirectional PCMA/RFC4733 RTP ========>|
```

This FXS-style H.248 line does not provide the MG with a distinct remote-answer
event. The gateway sends SIP 183/200 when the Context and media are established,
and the carrier's ringback is delivered as in-band audio.

## 6. Media and DTMF

The SIP and H.248 legs use independent even RTP/odd RTCP port pairs:

```text
SIP PBX <-- RTP/RTCP --> gateway SIP leg
                              |
                         RTP/RTCP relay
                              |
Carrier <-- RTP/RTCP --> gateway H.248 leg bound to VRF
```

Key implementation requirements:

- bind the H.248-side socket to the carrier VRF or interface;
- use an enterprise address reachable through the main routing table for the
  SIP-side socket;
- learn symmetric RTP/RTCP peers from received packets;
- relay PCMA without transcoding;
- with `media.h248_dtmf_mode: rfc4733`, accept the actual `telephone-event`
  payload from the MGC's initial LocalDescriptor and rewrite the payload between
  the two legs, for example SIP PT102 and H.248 PT97;
- with `media.h248_dtmf_mode: inband`, advertise only PCMA/PT8 in the H.248 Add
  Reply and synthesize SIP-side RFC4733 events 0-15 as continuous G.711 A-law
  dual tones;
- extend the firewall range through `media.port_max + 1`, because that next
  port is used for RTCP.

The PBX must offer PCMA/G.711A. The current version does not transcode speech
between PCMU, Opus, G.722, and PCMA. `inband` generates PCMA audio only for DTMF
events; it is not speech Codec transcoding.

In anonymized live testing, the MGC's initial Add LocalDescriptor advertised a
dynamic payload, but the final Local/Remote Descriptor in a later Modify removed
`telephone-event` and retained only PCMA/PT8. The carrier IVR ignored even
well-formed RFC4733 packets. After switching to `inband`, the SIP PBX continued
to send RFC4733 while the carrier leg carried only PCMA dual tones, and IVR menu
selection worked.

## 7. Transaction reliability and release

Production interworking must correctly handle UDP retransmissions and delayed
messages:

- cache H.248 Transaction Replies and suppress duplicate requests;
- retransmit SIP INVITE client transactions and ACK final responses;
- cache SIP server transactions;
- complete CANCEL 200, INVITE 487, and non-2xx ACK handling;
- stop sending CANCEL after a final response arrives;
- release active calls before switching between primary and backup MGCs;
- reclaim the Context and media ports after BYE, CANCEL, H.248 `al/on`, or
  Subtract.

Production acceptance must include an immediate inbound call after release.
That test exposes retained Context, CANCEL, INVITE, or line state more reliably
than a single successful call.

## 8. Current implementation boundaries

The current stable release supports the live-line-validated UDP/Text/PCMA
scenario. These capabilities are outside the current stable scope:

- H.248 ASN.1 binary encoding, TCP, or SCTP;
- SIP TCP/TLS and SRTP;
- SIP trunk Digest registration or authentication;
- T.38 fax;
- mid-call Codec transcoding;
- multiple physical Terminations shared by one process instance.

If any of these capabilities is required, extend the protocol state machine and
automated tests before production validation. Declaring support only through a
configuration setting is not sufficient.
