[English](SIP-PBX-INTEROP.md) | [中文](SIP-PBX-INTEROP.zh-CN.md)

# SIP PBX 对接说明

本文说明任意 SIP PBX 如何通过无注册、源 IP 识别的 SIP 中继对接
`h248-sip-gateway`。Huawei VRP、Asterisk/PJSIP 和 FreePBX 配置均为
匿名化通用参考，不包含生产地址、端口或号码。

## 1. 对接模型

```text
运营商呼入：H.248 MGC → gateway → SIP INVITE → PBX 入局路由 → 分机/队列
企业外呼：分机 → PBX 出局路由 → SIP INVITE → gateway → H.248 MGC
```

网关不向 PBX 注册。双方根据固定源 IP 建立 SIP 中继：

- PBX 将网关 IP:端口定义为无注册 SIP Trunk；
- 网关用 `sip.outbound_proxy` 指向 PBX 的中继监听地址；
- PBX 只接受来自网关 IP 的入局 SIP；
- 网关主机防火墙只接受来自 PBX IP 的 SIP/RTP。

## 2. SIP 与媒体参数

| 参数 | 推荐值 |
|---|---|
| SIP 传输 | UDP |
| SIP 中继认证 | 无注册、源 IP 识别 |
| Codec | PCMA/G.711A，payload 8 |
| Packetization | 20 ms |
| DTMF | RFC4733/RFC2833 telephone-event |
| SIP RTP | PBX 和网关之间可路由的实际地址 |
| NAT | 尽量禁用；如存在必须保证 Via/Contact/SDP 都可达 |
| Early media | 允许 183 + SDP 和带内回铃音 |
| CANCEL | 必须正确返回 200，并对原 INVITE 返回 487 |
| Re-INVITE/UPDATE | 允许媒体更新；不要强制转成不支持的 Codec |

运营商侧固定使用 PCMA 时，PBX 必须允许 PCMA。即使 PBX 终端同时提供
Opus、G.722 或 PCMU，也应保留 PCMA；网关不做编解码转码。

## 3. 网关 SIP 配置

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

字段关系：

- `listen`：PBX 外呼 INVITE 的目的地址；
- `advertised_address`：网关放入 Via/Contact 的地址；
- `trunk_uri`：运营商呼入时，网关生成的 SIP Request-URI 和初始 To；
- `outbound_proxy`：该 INVITE 实际发送到的 PBX 地址。
- `media.h248_dtmf_mode`：只控制网关发往 H.248 运营商侧的 DTMF 表示；
  PBX 侧始终可以继续使用 RFC4733/RFC2833。只有最终 H.248 媒体描述符
  不接受 telephone-event 时才使用 `inband`。

Request-URI 和实际发送地址可以不同。例如 PBX 要求 URI 是
`sip:6001@pbx.example`，但无注册中继监听在 `PBX_IP:5061`。

## 4. PBX 通用配置步骤

1. 创建一个 UDP、无注册、按源 IP 识别的 SIP 中继；
2. 中继对端填写网关的 `sip.listen` 地址；
3. 只启用 PCMA/G.711A，或至少把 PCMA 放在优先列表；
4. DTMF 选择 RFC4733/RFC2833；
5. 创建入局路由，把 `sip.trunk_uri` 中的号码送到目标分机、振铃组或 IVR；
6. 创建出局路由，把允许的号码模式送到该中继，不要自动添加未约定的出局字冠；
7. 确保 PBX 的内部号码拨号计划确实包含目标分机范围；
8. 限制该中继只接受网关源 IP，限制呼叫号码范围和并发数；
9. PBX SDP 必须公布网关可达的 RTP 地址，而不是管理地址或不可达私网地址。

## 5. Huawei VRP 配置框架

以下是经过匿名化处理的命令框架。命令树会随型号、VRP 版本和许可证变化，
请用设备的上下文帮助确认后再提交：

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

关键点：

- 华为 `pbxuser` 存在并注册，不代表其号码已经进入拨号计划；
- 必须有匹配内部分机范围的 `callprefix INTERNAL`；
- `attribute 0` 是 `Internal dialing`，不能误用 `attribute 1 Local dialing`；
- 缺少内部 callprefix 时，华为返回 `404 Not Found` 和
  `Q.850 cause=1 Unallocated number`；
- 中继的 Enterprise、Dn-set 与 PBX 用户必须一致；
- `display voice trace` 中的 `Callee number analysis failed` 是最直接的
  诊断证据。

外拨号码分析应按企业所在地和运营商工单拆为窄范围 `callprefix`，全部指向
`H248GW`。不要把现场的客服、手机、长途或市话前缀写入公共仓库。若内部分机
前缀与外线号码前缀重叠，应把外线规则拆成更长、更具体的前缀，并分别验证
内部短号与外线长号。除非企业拨号策略明确要求，不要隐式增加或删除接入码。

Huawei 设备侧的匿名化配置框架见 `deploy/huawei-ar6121e/README.md`。

## 6. Asterisk/PJSIP 示例

以下示例使用网关源 IP 识别中继。替换所有大写占位符。

`pjsip.conf`：

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

如果配置解析器不允许 endpoint 与 aor 同名，可把 aor 改名为
`h248gw-aor`，并同步修改 endpoint 的 `aors=`。

`extensions.conf`：

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

网关对应配置：

```yaml
sip:
  trunk_uri: "sip:INBOUND_EXTENSION@PBX_IP"
  outbound_proxy: "PBX_IP:PBX_TRUNK_PORT"
```

## 7. FreePBX 对接要点

FreePBX 中创建 PJSIP Trunk：

- Authentication：None；
- Registration：None；
- SIP Server：网关 IP；
- SIP Server Port：网关 `sip.listen` 端口；
- Match (Permit)：网关 IP/32；
- Codecs：只启用 alaw，或把 alaw 放在第一位；
- DTMF Mode：RFC4733；
- Direct Media：关闭；
- From Domain/Contact User：按本地号码计划设置，不要把号码改成网关无法解析的
  字符串。

随后创建：

- Inbound Route：DID 等于网关 `trunk_uri` 的用户部分，目的地为分机/队列；
- Outbound Route：允许的移动号码或 E.164 模式，Trunk 选择 H248GW。

## 8. 呼叫验收

| 测试 | 预期结果 |
|---|---|
| PBX 外呼 | 100/183/200/ACK，双向 PCMA RTP |
| PBX 侧挂机 | SIP BYE，H.248 `al/on`，资源释放 |
| 运营商侧挂机 | H.248 释放，网关向 PBX 发 BYE |
| 外线呼入 | 100/180/200，分机振铃并可双向通话 |
| 振铃中外网挂机 | CANCEL 200、INVITE 487、ACK |
| 挂机后立即再次呼入 | 新 Context，正常 180，不返回忙 |
| DTMF | `rfc4733` 时 payload 正确协商/转换；`inband` 时运营商侧只出现 PCMA 双音且 IVR 响应 |

## 9. 常见故障

### 404 / Q.850 cause 1

PBX 已收到中继 INVITE，但被叫号码不在拨号计划。检查：

- Request-URI 和 To 的用户部分；
- PBX 入局 DID；
- 内部分机前缀；
- Enterprise/Dn-set/tenant；
- 中继 callsource 或入局号码映射。

### 486 或挂机后一直忙

检查前一个呼叫是否完成：

- SIP BYE 或 CANCEL；
- CANCEL 的 200 和 INVITE 的 487/ACK；
- H.248 `al/on` Reply；
- Context、逻辑中继电路和 RTP 端口回到 Idle；
- PBX 是否重传旧 INVITE。

### 单通或无声

检查双方 SDP：

- `c=` 地址是否从对端网络可达；
- RTP 端口是否在防火墙允许范围；
- PBX 是否选中 PCMA/PT8；
- PBX 是否把终端不可达地址直接透传给网关；
- 网关日志中两方向 RTP 包计数是否都增长。

### 呼入正常、外呼无进展

检查运营商 DigitMap、摘机稳定窗口、被叫号码格式和 H.248 `dd/ce` 送号完成
事件。不要在网关刚 ServiceChange 完成的同一毫秒发起第一通外呼。

### 通话正常但 IVR 不响应按键

按媒体实际协商结果排查，不要只看 PBX 页面上的 DTMF 模式：

1. 检查 SIP offer/answer 中 `telephone-event` 的 payload 和 8000 Hz 时钟；
2. 抓取 PBX→网关 RTP，确认按键产生短的 RFC4733 事件包，事件码、duration
   和 End 位完整；
3. 检查 MGC 最后一次 H.248 Modify 的 Local/Remote Descriptor，而不只是
   首次 Add；确认最终描述符是否仍包含 `telephone-event`；
4. 在 `rfc4733` 模式下，确认网关→运营商事件包使用 MGC 最终接受的 payload；
5. 如果 RFC4733 包完整到达但 IVR 仍无响应，或最终 H.248 SDP 只保留
   PCMA/PT8，把 `media.h248_dtmf_mode` 改为 `inband`，校验配置并重启。

`inband` 模式下，SIP PBX 仍发送 RFC4733；网关将事件 0–15 合成 PCMA
双音，H.248 Add Reply 不再广告 `telephone-event`。抓包时运营商方向应只有
常规 172-byte（12-byte RTP 头 + 160-byte G.711A）的 PCMA RTP，而不应再
出现 16-byte telephone-event RTP。该模式已在匿名化运营商 IVR 上实测通过。

## 10. 安全建议

- PBX 和网关双方都按源 IP 限制 SIP；
- 只允许业务需要的出局号码模式；
- 限制中继并发数；
- 配置和日志目录只允许 root/服务账户读取；
- 不把 SIP 分机密码、路由器配置、运营商工单或 PCAP 提交到 Git；
- 对公网或跨不可信网络的部署，应在实现 SIP TLS/SRTP 或外层 VPN 后再使用。
