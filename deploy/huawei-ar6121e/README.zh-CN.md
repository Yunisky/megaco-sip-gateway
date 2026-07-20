[English](README.md) | [中文](README.zh-CN.md)

# Huawei AR6121E SIP PBX 对接

本目录提供一个把 Huawei AR6121E/VRP 语音服务连接到
`h248-sip-gateway` 的匿名化示例，其中不包含客户、运营商、路由器、分机或
生产网络标识。

`gateway.example.yaml` 中的所有地址都属于 RFC 5737 文档地址范围，部署前
必须替换。端口是标准端口或用于说明的示例端口，并非复制自真实环境。

## 拓扑

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

网关不会注册到 Huawei PBX。Huawei 中继通过网关的固定源地址识别网关。
终端 SIP 分机仍按正常方式注册到 PBX。

## 网关配置

复制匿名化示例并替换每一个文档地址：

```sh
cp deploy/huawei-ar6121e/gateway.example.yaml gateway.yaml
/usr/local/bin/h248-sip-gateway -config gateway.yaml -check-config
```

信令目的地址和被叫号码 URI 有意相互独立：

- `sip.outbound_proxy` 是 Huawei 无注册中继的监听地址；
- `sip.trunk_uri` 携带入局 DID 或分机号，必须能被 Huawei 被叫号码分析匹配；
- `sip.listen` 是 Huawei 发送出中继呼叫的目标地址。

对于最终 H.248 Local/Remote Descriptor 删除或忽略 `telephone-event` 的
运营商，使用：

```yaml
media:
  h248_dtmf_mode: "inband"
```

Huawei PBX 仍可发送 RFC4733。此时网关在 H.248 侧只报价 PCMA/PT8，并为
运营商 IVR 合成 G.711 A-law DTMF 双音。当运营商接受协商得到的
telephone-event RTP 时，使用默认 `rfc4733` 模式。

## Huawei 语音配置框架

具体命令树会随 VRP 版本和许可证变化。以下只是包含占位符的配置框架；替换
每个大写 token，并使用设备的上下文帮助核对命令后再提交配置：

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

重要 Huawei 行为：

- 注册成功的 `pbxuser` 不会自动使其号码可拨，必须创建匹配的内部
  `callprefix`；
- VRP `attribute 0` 表示内部拨号；运营商出局路由应使用当前版本对应的本地或
  长途类别；
- 缺少被叫号码路由通常会返回 SIP `404 Not Found` 和 Q.850 cause 1，
  `display voice trace` 可能显示 `Callee number analysis failed`；
- 中继 Enterprise、Dn-set、callsource 和分机对象必须相互一致；
- 只有在呼入、外呼、释放和立即重拨测试全部通过后，才保存运行配置。

## 外拨号码分析

创建范围严格的 `callprefix` 组，把每一类允许号码路由到 `H248GW`。典型号码
计划可能包含：

| 号码类型 | 模式 | 总长度示例 |
|---|---|---:|
| 客户/服务号码 | 配置的服务前缀 | 取决于运营商 |
| 全国服务号码 | 配置的全国前缀 | 取决于运营商 |
| 手机号码 | 全国移动号码前缀 | 11 |
| 长途手机号码 | 长途字冠加手机号码 | 12 |
| 本地固话号码 | 非零开头 | 8 |

除非添加或删除接入码明确属于企业拨号策略，否则不要执行此类转换。如果内部
分机前缀与外部固话前缀重叠，应把外部路由拆成更长、更具体的前缀，使内部
路由保持明确。

## 防火墙

使用地址对象，不要在文档中写入真实地址：

```text
PBX_IP/32 -> GATEWAY_SIP_IP:GATEWAY_SIP_PORT/udp
PBX_IP/32 -> GATEWAY_SIP_IP:GATEWAY_RTP_RANGE/udp
MGC_ADDRESS_SET -> GATEWAY_H248_IP:H248_PORT/udp
CARRIER_MEDIA_SET -> GATEWAY_H248_IP:H248_RTP_RANGE/udp
```

RTP 分配器会保留一个偶数 RTP 端口和紧随其后的奇数 RTCP 端口。如果
`media.port_max` 是最后一个偶数 RTP 端口，防火墙必须放行到
`media.port_max + 1`。

## 验收测试

进入生产前验证以下全部项目：

1. PBX 中继状态正常且目标分机已注册。
2. PBX 发起的呼叫到达运营商并通过双向 PCMA 语音。
3. 运营商发起的呼叫产生 SIP 100/180/200 并通过双向语音。
4. PBX 和运营商分别主动释放时，均能拆除 SIP、H.248 Context、RTP 和 RTCP。
5. 释放后立即再次呼入能够振铃，不返回忙。
6. 运营商取消未应答呼叫时完成 SIP CANCEL/487/ACK。
7. DTMF 能够操作 IVR。在 `inband` 模式下，运营商侧应出现 PCMA 双音包且
   不应出现 telephone-event 包。

切勿提交路由器凭据、分机密码、导出的配置、抓包、生产日志或回退文件路径。
