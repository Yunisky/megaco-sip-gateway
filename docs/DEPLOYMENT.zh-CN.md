[English](DEPLOYMENT.md) | [中文](DEPLOYMENT.zh-CN.md)

# H.248↔SIP 网关部署说明

本文适用于从 GitHub Release 在独立 Linux VM 或 host-network 容器中部署
一条 H.248 外线。生产推荐 systemd + 独立 VM；每条外线部署一个实例。

## 1. 部署前需要确认的参数

| 类别 | 必填信息 |
|---|---|
| 企业侧 | 网关 SIP IP、PBX IP/端口、入局目标号码、SIP RTP 地址和端口池 |
| 运营商侧 | 本地 H.248 IP/掩码/网关、VRF/接口、主备 MGC、MGID、Termination |
| 协议 | H.248 版本/传输/编码、ServiceChange、Codec、DTMF payload、ptime |
| 安全 | PBX 与 MGC 源地址白名单、SIP/H.248/RTP 防火墙范围 |

当前稳定版要求：

- Linux + systemd；
- H.248 UDP Text v1-v3；已实线验证的是 v1；
- SIP UDP；
- PBX 与运营商都支持 PCMA/G.711A；
- 运行账户可获得 `CAP_NET_RAW`，以便使用 `SO_BINDTODEVICE`；
- H.248 IP、SIP IP 和 SDP 中公布的地址对对应网络真实可达。

## 2. 推荐网络结构

```text
企业网络 / 主路由表
  GATEWAY_SIP_IP
  SIP_PORT/UDP + SIP侧 RTP
          |
      gateway VM
          |
  vrf-h248 / 运营商专网
  H248_PORT/UDP + 运营商侧 RTP
```

运营商网卡应放入单独 VRF，避免运营商私网地址、默认路由或重复网段污染管理
网络。下面是一次性 `iproute2` 示例，实际接口名、地址和表号必须替换：

```sh
ip link add vrf-h248 type vrf table 248
ip link set vrf-h248 up
ip link set CARRIER_IFACE master vrf-h248
ip addr add GATEWAY_H248_IP/CARRIER_PREFIX dev CARRIER_IFACE
ip link set CARRIER_IFACE up
ip route add table 248 default via CARRIER_GATEWAY dev CARRIER_IFACE
ip vrf exec vrf-h248 ping -c 3 PRIMARY_MGC_IP
```

这些命令重启后不会自动保留。请使用发行版的 NetworkManager、systemd-networkd
或网络配置系统持久化。远程服务器上修改 VRF 前必须保留带外管理或回退窗口。

## 3. 获取并校验 Release

以 `${VERSION}`、Linux amd64 为例：

```sh
VERSION=vX.Y.Z
curl -fLO "https://github.com/Yunisky/megaco-sip-gateway/releases/download/${VERSION}/h248-sip-gateway-${VERSION}-linux-amd64.tar.gz"
curl -fLO "https://github.com/Yunisky/megaco-sip-gateway/releases/download/${VERSION}/checksums.txt"
sha256sum -c checksums.txt --ignore-missing
tar -xzf "h248-sip-gateway-${VERSION}-linux-amd64.tar.gz"
cd "h248-sip-gateway-${VERSION}-linux-amd64"
```

arm64 主机将文件名中的 `amd64` 改为 `arm64`。也可以只下载
`install.sh`；脚本会按主机架构下载 Release 二进制和 systemd unit，并使用
Release 的 `checksums.txt` 验证二进制。

## 4. 准备 gateway.yaml

复制模板：

```sh
cp gateway.example.yaml gateway.yaml
chmod 600 gateway.yaml
vi gateway.yaml
```

生产最重要的字段如下：

| 字段 | 含义 |
|---|---|
| `sip.listen` | 网关在企业侧监听的 IP:端口 |
| `sip.advertised_address` | SIP Via/Contact 公布的可达 IP:端口 |
| `sip.domain` | 网关 SIP 域或地址 |
| `sip.trunk_uri` | 运营商呼入时送给 PBX 的 Request-URI |
| `sip.outbound_proxy` | SIP 报文实际发送到的 PBX IP:端口 |
| `h248.listen` | 运营商分配给 MG 的本地 IP:端口 |
| `h248.bind_device` | 运营商网卡或 VRF master，例如 `vrf-h248` |
| `h248.mg_id` | 线上完整 MID，可能是 `[IP]:port`，不一定等于运营商工单中的逻辑 ID |
| `h248.mgc` / `backup_mgc` | 主备 MGC IP:端口 |
| `h248.physical_termination` | 物理线路，例如 `A0` |
| `media.rtp_ip` | SIP/PBX 侧公布的 RTP 地址 |
| `media.h248_rtp_ip` | 运营商 H.248 侧公布的 RTP 地址 |
| `media.port_min/max` | 网关分配的偶数 RTP 端口范围；RTCP 使用下一个奇数端口 |
| `media.h248_dtmf_mode` | `rfc4733` 动态协商/重写事件，或 `inband` 把 SIP RFC4733 合成为 H.248 侧 PCMA 双音 |

匿名化配置示例可参考：

- `deploy/spes/gateway.example.yaml`：双网卡/VRF H.248 参数；
- `deploy/huawei-ar6121e/gateway.example.yaml`：华为 PBX 对接方式。

示例中的 RFC 5737 地址不可用于生产。每条线路的 MGID、MGC、PBX、VRF 和
媒体地址必须按工单填写。

## 5. 一键安装

首次安装：

```sh
sudo ./install.sh --version "${VERSION}" --config ./gateway.yaml
```

如果使用解压后的离线包，脚本会自动使用包内二进制和 unit，不访问网络：

```sh
sudo ./install.sh --config ./gateway.yaml
```

脚本依次执行：

1. 检测 Linux、systemd 和 amd64/arm64 架构；
2. 选择包内二进制或下载指定 Release；
3. 校验 SHA-256；
4. 用候选二进制执行 `-check-config`；
5. 创建无登录权限的 `h248gw` 系统账户；
6. 把现有二进制、配置和 unit 备份到仅 root 可读的时间戳目录；
7. 安装二进制、0640 配置和 systemd unit；
8. 再次校验已安装配置；
9. daemon-reload、enable、restart，并确认服务为 active。

脚本不会修改接口、VRF、路由、DNS 或防火墙。仅安装、不启动：

```sh
sudo ./install.sh --config ./gateway.yaml --no-start
```

## 6. 升级和回退

升级到指定 Release 并保留当前配置：

```sh
sudo ./install.sh --version "${VERSION}"
```

替换配置并升级：

```sh
sudo ./install.sh --version "${VERSION}" --config ./gateway.yaml
```

每次安装的备份位于：

```text
/var/lib/h248-sip-gateway/backups/YYYYMMDDTHHMMSSZ/
```

手工回退示例：

```sh
systemctl stop h248-sip-gateway
install -m 0755 BACKUP/h248-sip-gateway /usr/local/bin/h248-sip-gateway
install -o root -g h248gw -m 0640 BACKUP/gateway.yaml /etc/h248-sip-gateway/gateway.yaml
install -m 0644 BACKUP/h248-sip-gateway.service /etc/systemd/system/h248-sip-gateway.service
systemctl daemon-reload
systemctl restart h248-sip-gateway
```

## 7. 防火墙

最小放行原则：

- 企业侧只允许 PBX IP 访问网关 SIP UDP 端口；
- 企业侧只允许 PBX IP 访问网关 RTP/RTCP 端口池；
- H.248 VRF 只允许主备 MGC 访问 H.248 UDP 端口；
- H.248 VRF 允许运营商媒体地址访问 RTP/RTCP 端口池；
- 不要把 SIP 5060 或大段 RTP 端口直接暴露给不可信网络。

防火墙应从 `media.port_min` 覆盖到 `media.port_max + 1`，最后一个奇数
端口用于 RTCP。不要照抄其他部署的端口池。

## 8. 部署后检查

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

启动日志必须看到：

- ROOT ServiceChange Reply；
- 物理 Termination ServiceChange Reply；
- `h248 registration active`；
- 初始 `al/on` Reply；
- 没有 H.248 error descriptor。

## 9. 生产验收矩阵

至少执行：

1. PBX 分机外呼、接通、双向语音；
2. PBX 侧主动挂机；
3. 运营商侧主动挂机；
4. 外线呼入、PBX 振铃、接听、双向语音；
5. 振铃未接时外网挂机，确认 `CANCEL/200/487/ACK`；
6. 已接通呼叫释放后 2～5 秒立即再次呼入，确认不忙；
7. 主 MGC 不可达时验证备 MGC，但必须在维护窗口执行；
8. 重启网关后确认自动 ServiceChange 和线路恢复。

日志中的 RTP 双向计数都应大于零。测试结束后 PBX 用户、H.248 Context、
逻辑中继电路和 RTP 端口必须回到 Idle/空闲。

## 10. Docker

容器必须使用 host networking，并能看到宿主机 VRF；还需要
`CAP_NET_RAW`。建议一条外线一个容器。示意：

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

如果容器平台无法提供 host networking、VRF 可见性和
`SO_BINDTODEVICE`，应使用独立 VM。

## 11. 故障排查

- 没有 ServiceChange Reply：检查 VRF 路由、源 IP、MGC IP/端口、MID 和
  运营商 ACL；
- PBX 返回 404/Q.850 cause 1：检查 PBX 入局号码、拨号计划、企业/DN-set
  和内部号码 callprefix；
- 单通：检查两侧 SDP `c=` 地址、RTP 防火墙、VRF 绑定和双向 RTP 计数；
- 挂机后忙：检查 `al/on` Reply、BYE/CANCEL、487 ACK、Context 释放和
  INVITE 事务是否到期；
- 配置有效但服务启动失败：检查本地 IP 是否已经配置、端口是否被占用，以及
  systemd unit 是否获得 `CAP_NET_RAW`。
