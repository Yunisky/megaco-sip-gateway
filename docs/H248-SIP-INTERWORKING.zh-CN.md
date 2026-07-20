[English](H248-SIP-INTERWORKING.md) | [中文](H248-SIP-INTERWORKING.zh-CN.md)

# H.248 转 SIP 互通实现总结

本文总结本项目中已经通过匿名化实线环境验证的 H.248/Megaco 与 SIP
互通方式。它描述的是网关实现和协议映射，不代替运营商针对具体线路下发的
参数表。文中不包含真实 MGC、MG、PBX 地址或用户号码。

## 1. 网关在两个网络中的角色

```text
运营商 H.248 MGC                     企业 SIP PBX
        |                                 |
        | H.248 信令                      | SIP 信令
        | 运营商 RTP                      | 企业 RTP
        v                                 v
             h248-sip-gateway
       H.248 侧充当 MG，SIP 侧充当 B2BUA
```

网关在运营商侧是 Media Gateway（MG），由运营商的 Media Gateway
Controller（MGC）控制。它负责 `ServiceChange`、物理 Termination、临时
RTP Termination、Context 和线路事件。

网关在企业侧是一个无注册 SIP 中继端点，同时锚定 SIP 与 H.248 两侧的
RTP/RTCP。SIP PBX 不需要理解 H.248，运营商 MGC 也不需要理解 SIP。

每条运营商外线建议使用独立 VM 或独立 host-network 容器。这样可以隔离：

- H.248 MGID、物理 Termination 和 Context 状态；
- 运营商 VRF、IP、路由和故障域；
- SIP 中继端口与 RTP 端口池；
- 单线升级、重启和回退。

## 2. 核心对象映射

| H.248 对象或事件 | SIP 对象或动作 |
|---|---|
| MG/MID | SIP 网关实例、Via/Contact 地址 |
| 物理 Termination，例如 `A0` | 一条运营商外线 |
| 临时 Termination，例如 `RTP/1` | 一个 SIP Dialog 的媒体腿 |
| Context | SIP Call-ID/Dialog 与内部 Call 状态 |
| `Add`/`Modify` + SDP | INVITE/183/200/UPDATE/re-INVITE + SDP |
| `al/of` | 摘机或接听监控；按呼叫方向驱动应答状态 |
| `al/on` | 挂机；映射为 BYE、CANCEL 或 Context 释放 |
| `andisp/dwa` | 主叫号码，映射到 SIP From/PAI |
| `dd/ce` + DigitMap | SIP 被叫号码送号完成 |
| H.248 Transaction Reply | SIP 事务推进或错误释放 |

网关不是简单地把文本报文改写成另一种文本报文。H.248 与 SIP 的事务模型、
呼叫模型和媒体更新顺序不同，因此网关内部必须维护独立状态机，并将一个
H.248 Context 与一个 SIP Dialog 关联起来。

## 3. 启动和线路注册

生产实例启动后依次执行：

1. 向主 MGC 发送 ROOT `ServiceChange/Restart`；
2. ROOT 成功后，对物理 Termination 发送 `ServiceChange/Restart`；
3. 发送初始 `al/on`，把线路同步为空闲挂机状态；
4. 进入稳定窗口，之后才允许 SIP 侧发起外呼；
5. 按 MGC 活动超时检测主 MGC，必要时释放呼叫并切换到备 MGC。

一个匿名化实线环境中观察到的协议特征是：

| 参数 | 观察值 |
|---|---|
| H.248 传输 | UDP |
| H.248 编码 | Text |
| H.248 版本 | v1 |
| MGC | 运营商下发的主、备地址，未公开 |
| MGID | 完整 IP/端口形式，具体值未公开 |
| ServiceChange | `Restart`, `901 Cold Boot`,无 Profile |
| 物理 Termination | `A0` |
| 临时 Termination 前缀 | `RTP/` |
| 编解码 | PCMA/PT8, 20 ms |
| DTMF | 初始描述符可含 telephone-event；最终描述符可能仅保留 PCMA |

这些值是线路配置，不应硬编码在程序中；每个实例通过 `gateway.yaml` 设置。

## 4. 运营商呼入到 SIP PBX

呼入的主要流程是：

```text
MGC                         Gateway                         SIP PBX
 | Add A0 + $ + remote SDP     |                               |
 |---------------------------->|                               |
 | Add Reply + RTP/n + SDP     |                               |
 |<----------------------------|                               |
 | andisp/dwa 主叫号码          |                               |
 |---------------------------->| INVITE 被叫分机 + PAI + SDP  |
 |                             |------------------------------>|
 |                             | 100 / 180                     |
 |                             |<------------------------------|
 |                             | 200 OK + SDP                  |
 |                             |<------------------------------|
 | al/of 应答事件               | ACK                           |
 |<----------------------------|------------------------------>|
 |<=================== 双向 PCMA/RFC4733 RTP =================>|
```

SIP Request-URI 使用 `sip.trunk_uri`，实际 UDP 目的地址使用
`sip.outbound_proxy`。两者故意分开：PBX 可能要求 URI 使用本地域，但信令
报文必须发往一个专用无注册中继端口。

如果 PBX 返回最终非 2xx 响应，网关发送相应 ACK，通知 H.248 侧挂机并释放
Context。运营商在 SIP 应答前释放时，网关发送 CANCEL，并完整处理
`200 CANCEL + 487 INVITE + ACK`。

## 5. SIP PBX 外呼到运营商

SIP PBX 把号码作为 Request-URI 用户部分发送到网关：

```text
SIP PBX                     Gateway                         MGC
 | INVITE number + SDP        |                              |
 |--------------------------->| 100 Trying                   |
 |<---------------------------|                              |
 |                            | al/of 摘机                   |
 |                            |----------------------------->|
 |                            | DigitMap/收号指令            |
 |                            |<-----------------------------|
 |                            | dd/ce 送完整号码             |
 |                            |----------------------------->|
 |                            | Add A0 + $ / Context         |
 |                            |<---------------------------->|
 | 183 + SDP                  |                              |
 |<---------------------------|                              |
 | 200 OK + SDP               |                              |
 |<---------------------------|                              |
 | ACK                        |                              |
 |--------------------------->|                              |
 |<================== 双向 PCMA/RFC4733 RTP =================>|
```

该 FXS 风格 H.248 线路没有向 MG 提供独立的远端应答事件。网关在 Context
和媒体建立时发送 SIP 183/200，运营商回铃音作为带内音频传送。

## 6. 媒体和 DTMF

SIP 和 H.248 两侧分别分配独立的偶数 RTP/奇数 RTCP 端口对：

```text
SIP PBX <-- RTP/RTCP --> gateway SIP leg
                              |
                         RTP/RTCP relay
                              |
Carrier <-- RTP/RTCP --> gateway H.248 leg bound to VRF
```

关键实现要求：

- H.248 侧 socket 绑定运营商 VRF 或接口；
- SIP 侧 socket 使用主路由表可达的企业地址；
- 根据收到的数据包进行对称 RTP/RTCP 对端学习；
- PCMA 直接转发，不进行转码；
- `media.h248_dtmf_mode: rfc4733` 时，网关从 MGC 的首次
  LocalDescriptor 接受实际 `telephone-event` payload，并在两侧重写，
  例如 SIP PT102 与 H.248 PT97；
- `media.h248_dtmf_mode: inband` 时，H.248 Add Reply 只报价 PCMA/PT8，
  网关把 SIP 侧 RFC4733 事件 0–15 合成为连续的 G.711 A-law 双音；
- 防火墙端口范围必须覆盖 `media.port_max + 1`，因为后一个端口是 RTCP。

PBX 必须提供 PCMA/G.711A。当前版本不进行 PCMU、Opus、G.722 与 PCMA
之间的通话编解码转码。`inband` 仅对 DTMF 事件生成 PCMA 音频，不是对
通话语音做 Codec 转码。

匿名化实测中，MGC 首次 Add 的 LocalDescriptor 曾报价动态 payload，但随后
Modify 的最终 Local/Remote Descriptor 删除了 `telephone-event`，只保留
PCMA/PT8。即使网关转发格式完整的 RFC4733 包，运营商 IVR 也不响应；切换
为 `inband` 后，SIP PBX 仍发送 RFC4733，而运营商侧只收到 PCMA 双音，IVR
菜单选择正常。

## 7. 事务可靠性和释放

生产互通必须正确处理 UDP 重传和延迟报文：

- H.248 Transaction Reply 缓存与重复请求抑制；
- SIP INVITE 客户端重传和最终响应 ACK；
- SIP 服务端事务缓存；
- CANCEL 的 200、INVITE 的 487 及非 2xx ACK；
- 最终响应到达后不再重复 CANCEL；
- 主/备 MGC 切换时先释放活动呼叫；
- BYE、CANCEL、H.248 `al/on` 或 Subtract 后统一回收 Context 与媒体端口。

生产验收中必须做“挂机后立即再次呼入”。这比只验证一次接通更容易发现
Context、CANCEL、INVITE 或线路状态残留。

## 8. 当前实现边界

当前稳定版本支持经过实线验证的 UDP/Text/PCMA 场景。以下能力尚不属于
当前稳定范围：

- H.248 ASN.1 二进制编码、TCP 或 SCTP；
- SIP TCP/TLS、SRTP；
- SIP 中继 Digest 注册或认证；
- T.38 传真；
- 中途编解码转码；
- 多条物理 Termination 共享同一个进程实例。

需要这些能力时，应先扩展协议状态机和自动化测试，再进入生产验证，不能只
通过配置声明“支持”。
