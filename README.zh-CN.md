[English](README.md) | [中文](README.zh-CN.md)

# H.248 ↔ SIP 网关

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

`h248-sip-gateway` 是一个运行在 MG 侧的 H.248/Megaco 到 SIP 互通网关，
用于把 SIP IP-PBX 接入由 H.248 控制的运营商接入线路。每条外线都可以运行在
独立 VM 或使用 host networking 的容器中，使运营商路由、VRF 状态、呼叫状态
和故障相互隔离。

本实现已经在真实运营商线路和硬件 SIP PBX 上完成互通验证。所有公开示例均已
匿名化：看似可路由的地址均属于 RFC 5737 文档地址范围，部署前必须替换。

## 文档

- H.248/SIP 信令、呼叫状态、媒体、释放和 DTMF 映射：
  [English](docs/H248-SIP-INTERWORKING.md) |
  [中文](docs/H248-SIP-INTERWORKING.zh-CN.md)；
- VM/容器网络、systemd 安装、回退、防火墙和验收测试：
  [English](docs/DEPLOYMENT.md) | [中文](docs/DEPLOYMENT.zh-CN.md)；
- 通用 SIP 中继、Huawei VRP、Asterisk/PJSIP 和 FreePBX 对接：
  [English](docs/SIP-PBX-INTEROP.md) |
  [中文](docs/SIP-PBX-INTEROP.zh-CN.md)；
- 匿名化双网卡 Linux/VRF 部署示例：[English](deploy/spes/README.md) |
  [中文](deploy/spes/README.zh-CN.md)；
- 匿名化 Huawei AR6121E 对接框架：
  [English](deploy/huawei-ar6121e/README.md) |
  [中文](deploy/huawei-ar6121e/README.zh-CN.md)；
- 可复现的 Asterisk 实验室 PBX：
  [English](deploy/asterisk-lab/README.md) |
  [中文](deploy/asterisk-lab/README.zh-CN.md)。

## 已实现能力

- H.248 v1-v3 Text over UDP；实线互通使用 v1 完成验证。
- 支持嵌套 Transaction → Context → Command 解析、紧凑和完整命令名、单个
  Transaction 内多个 Context 以及可选命令。
- MG 侧 ROOT 和物理 Termination 的 `ServiceChange/Restart`。
- 按顺序向主备 MGC 注册、有界重试、活动超时、活动呼叫拆除和故障切换。
- H.248 Reply 原文缓存和重复请求抑制。
- 处理 `ServiceChange`、`Add`、`Modify`、`Subtract`、`Notify`、
  `AuditValue` 和 `AuditCapabilities`。
- 物理和临时 Termination 映射、`andisp/dwa` 主叫号码解码，以及通过
  `dd/ce` 完成可配置运营商 DigitMap 收号。
- 双向 H.248 Context ↔ SIP Dialog 状态映射。
- SIP/UDP INVITE、临时/最终响应、ACK、PRACK、CANCEL、BYE、re-INVITE、
  UPDATE、OPTIONS 和 INFO。
- SIP 服务端事务响应缓存和未应答 INVITE 重传。
- 正确处理 CANCEL 200、INVITE 487 和非 2xx ACK。
- 跨企业主路由表与运营商 VRF 锚定 RTP/RTCP。
- 独立的偶数/奇数 RTP/RTCP 端口对、对称对端学习、PCMA/PT8 转发和 RTCP
  转发。
- RFC4733/RFC2833 telephone-event payload 协商和重写。
- 对最终 H.248 媒体描述符删除或忽略 `telephone-event` 的运营商 IVR，
  可选将 RFC4733 合成为 PCMA 带内 DTMF。
- Linux `SO_BINDTODEVICE`，用于 H.248 信令和运营商侧媒体。
- 有界 H.248 和 SIP 探测、带认证的 SIP REGISTER 探测、systemd 部署、
  host-network 容器镜像和 Asterisk 实验室 PBX。

## 呼叫映射

运营商发起的呼叫：

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

PBX 发起的呼叫：

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

部分 FXS 风格 Profile 通过带内音频提供呼叫进展，并且没有独立的远端应答
事件。对这类 Profile，网关在 H.248 Context 分配时发送 SIP 183，在 MGC
激活双向媒体时发送 SIP 200。

## 构建和测试

建议使用 Go 1.22 或更高版本：

```sh
go test ./...
go test -race ./...
go vet ./...
./scripts/check-public-tree.sh
go build ./cmd/gateway
go build ./cmd/h248-call-probe
go build ./cmd/sip-register-probe
```

测试使用回环 UDP socket 覆盖 H.248 注册/故障切换、SIP 事务、呼入和外呼、
RTP/RTCP 转发、动态 telephone-event payload 映射以及 RFC4733 到 PCMA 的
DTMF 合成。自动化测试不需要运营商线路或 PBX。

## 配置

把公开模板复制为已被 Git 忽略的运行时配置：

```sh
cp gateway.example.yaml gateway.yaml
go run ./cmd/gateway -config gateway.yaml -check-config
go run ./cmd/gateway -config gateway.yaml
```

重要参数：

- `sip.listen`：网关在 PBX 侧网络的监听地址；
- `sip.advertised_address`：写入 SIP Via 和 Contact 的地址；
- `sip.trunk_uri`：运营商呼入时生成的 Request-URI；
- `sip.outbound_proxy`：PBX SIP 中继监听地址；
- `h248.listen`：运营商侧本地 H.248 地址；
- `h248.bind_device`：运营商接口或 VRF master；
- `h248.mg_id`：运营商分配的完整线上 mId；
- `h248.mgc` / `h248.backup_mgc`：按顺序排列的 MGC 端点；
- `h248.physical_termination`：运营商物理线路 Termination；
- `media.rtp_ip`：PBX 侧 RTP 地址；
- `media.h248_rtp_ip`：运营商侧 RTP 地址；
- `media.dtmf_payload_type`：SIP 侧 telephone-event payload；
- `media.h248_dtmf_payload_type`：H.248 侧备用 telephone-event payload；
- `media.h248_dtmf_mode`：`rfc4733` 表示协商并重写事件，`inband` 表示将
  SIP 事件合成为 H.248 侧 PCMA 双音；
- `media.port_min` / `media.port_max`：偶数 RTP 分配范围。防火墙还需允许
  `media.port_max + 1` 这个奇数 RTCP 端口。

在 Linux 上绑定设备或 VRF 需要 root 或 `CAP_NET_RAW`。随项目提供的
systemd unit 会把该 capability 授予专用服务账户。

## 探测工具

请使用占位符或明确分配给实验室的地址。以下是有界 MGC 注册探测示例：

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

有状态呼叫探测会与网关绑定相同的 H.248 源地址，因此使用前应先停止网关：

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

在不把密码放入进程参数的情况下验证 SIP registrar：

```sh
go run ./cmd/sip-register-probe \
  -target PBX_IP:SIP_PORT \
  -username TEST_EXTENSION \
  -password-file /path/to/extension.password \
  -hold 35s
```

对回环网关执行本地 SIP 发起呼叫探测：

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

只能使用明确授权用于测试的目的号码和运营商资源。

## Docker

```sh
docker compose up --build
```

镜像使用 host networking，因为 SIP 与 H.248 会在协议正文中携带网络地址，
且媒体可能跨越两个路由域。如果容器平台无法暴露宿主机 VRF 或
`SO_BINDTODEVICE`，优先使用 VM。

## 当前边界

当前实现不支持 H.248 ASN.1 二进制编码、H.248 TCP/SCTP、SIP TCP/TLS、
SRTP、T.38、SIP 中继 Digest 认证或通用媒体转码。PCMA 语音直接转发，不做
转码；带内 DTMF 模式只合成按键音。

## 隐私与安全

- 切勿提交 `gateway.yaml`、凭据、用户标识、运营商工单、导出的 CPE/路由器
  配置、抓包、日志或回退路径。
- 文档与测试 fixture 只能使用 RFC 5737 网络（`192.0.2.0/24`、
  `198.51.100.0/24` 和 `203.0.113.0/24`）。
- 按对端地址和满足业务所需的最小端口范围限制 SIP、H.248、RTP 和 RTCP。
- 在条件允许时，每条线路使用一个隔离实例。

## 许可证

本项目使用 [Apache License 2.0](LICENSE)。
