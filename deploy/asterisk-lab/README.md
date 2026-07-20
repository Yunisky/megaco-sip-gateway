# Asterisk lab IP-PBX

This deployment provides a temporary, reproducible SIP PBX for exercising the
H.248 interworking gateway. It is intentionally separate from the carrier VRF:
Asterisk binds the PBX-facing address and the gateway continues to bind H.248
signaling and carrier media to `vrf-h248`.

## Lab topology

| Function | Address |
|---|---|
| Asterisk SIP | `192.0.2.10:5062/udp` |
| Asterisk RTP | `192.0.2.10:10000-10199/udp` |
| Gateway SIP | `192.0.2.10:5060/udp` |
| Gateway H.248 | `198.51.100.10:2944/udp` in `vrf-h248` |

The PBX supplies two authenticated softphone accounts, `6001` and `6002`.
Their passwords are generated on the test host and must not be committed.

Dial plan:

- `6000`: ring group containing 6001 and 6002;
- `6001` / `6002`: direct extension calls;
- `600`: Asterisk Echo application for local audio testing;
- `9<number>`: strip 9 and send the remaining number to the H.248 gateway;
- carrier calls received from the gateway: ring 6001 and 6002.

## Build and install

The lab image uses Alpine 3.20 and installs Asterisk plus PJSIP from Alpine's
signed repository. Override the `BASE_IMAGE` build argument only when your
environment requires an approved registry mirror.

```sh
docker build \
  --file deploy/asterisk-lab/Containerfile \
  --tag h248-asterisk-lab:20 \
  deploy/asterisk-lab
```

Create host-only credentials and install the service:

```sh
install -d -o root -g root -m 0700 /etc/asterisk-lab
install -o root -g root -m 0600 \
  deploy/asterisk-lab/asterisk-lab.env.example \
  /etc/asterisk-lab/asterisk-lab.env

# Replace both example passwords before starting the service.
install -o root -g root -m 0644 \
  deploy/asterisk-lab/asterisk-lab.service \
  /etc/systemd/system/asterisk-lab.service
systemctl daemon-reload
systemctl enable --now asterisk-lab
```

Open only the PBX-facing firewalld zone:

```sh
firewall-cmd --permanent --zone=public --add-port=5062/udp
firewall-cmd --permanent --zone=public --add-port=10000-10199/udp
firewall-cmd --reload
```

Do not add these PBX ports to the carrier `h248` zone.

## Gateway route

Use the following values in the SPES gateway profile:

```yaml
sip:
  trunk_uri: "sip:6000@192.0.2.10:5062"
  outbound_proxy: "192.0.2.10:5062"
```

Validate both services before a carrier call:

```sh
docker exec h248-asterisk-lab asterisk -rx 'core show version'
docker exec h248-asterisk-lab asterisk -rx 'pjsip show endpoints'
docker exec h248-asterisk-lab asterisk -rx 'dialplan show lab-users'
systemctl status asterisk-lab h248-sip-gateway --no-pager
```

## Softphone settings

Configure a SIP softphone with:

- server/domain: `192.0.2.10`;
- transport: UDP;
- port: `5062`;
- username/authentication name: `6001` or `6002`;
- password: the corresponding host-only generated password;
- DTMF: RFC 4733/RFC 2833;
- preferred codec: PCMA/G.711 A-law.

Start with extension `600` to validate local two-way audio before placing an
outside call. This temporary PBX does not expose anonymous dialing, AMI, ARI,
or an HTTP management interface.

All checked-in addresses are from RFC 5737 documentation ranges. Replace them
before use and keep generated credentials and runtime captures outside Git.
