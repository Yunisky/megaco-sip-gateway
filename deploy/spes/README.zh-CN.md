[English](README.md) | [中文](README.zh-CN.md)

# 双网卡 SPES/openEuler 部署示例

这是一个具有独立企业 SIP 网络和运营商 H.248 网络的 VM 匿名化示例。所有
提交到仓库的地址均为 RFC 5737 文档地址。每次部署都必须重新核对接口名、
路由、VRF 表号、端口和地址范围。

## 示例结构

```text
enterprise interface -> main routing table -> SIP signaling and SIP RTP
carrier interface    -> vrf-h248          -> H.248 signaling and carrier RTP
```

示例 `gateway.example.yaml` 使用：

- 来自 `192.0.2.0/24` 的企业侧文档地址；
- 来自 `198.51.100.0/24` 的运营商侧文档地址；
- `vrf-h248` 作为 Linux bind device；
- H.248 text over UDP 和 PCMA 媒体；
- 两侧相互独立的偶数/奇数 RTP/RTCP 端口对。

安装前复制并编辑 Profile：

```sh
cp deploy/spes/gateway.example.yaml gateway.yaml
chmod 600 gateway.yaml
./h248-sip-gateway -config gateway.yaml -check-config
```

切勿在真实部署中路由 RFC 5737 地址。请把所有地址替换为分配给网关、PBX 和
MGC 的实际地址。

## VRF 检查清单

1. 为运营商接口创建专用 VRF 和路由表。
2. 只把运营商侧接口移入该 VRF。
3. 在 VRF 路由表中配置运营商网关和 MGC 路由。
4. 将管理网络和 PBX 的默认路由保留在主路由表中。
5. 把 `h248.bind_device` 设置为 VRF master。
6. 确认 `media.h248_rtp_ip` 属于运营商接口。
7. 确认 PBX 能通过主路由表到达 `media.rtp_ip`。

使用占位符的命令示例：

```sh
ip link add VRF_NAME type vrf table VRF_TABLE_ID
ip link set VRF_NAME up
ip link set CARRIER_IFACE master VRF_NAME
ip addr add CARRIER_IP/CARRIER_PREFIX dev CARRIER_IFACE
ip route add table VRF_TABLE_ID default via CARRIER_GATEWAY dev CARRIER_IFACE
ip vrf exec VRF_NAME ping -c 3 PRIMARY_MGC_IP
```

使用 NetworkManager、systemd-networkd 或发行版网络管理器持久化等价配置。
不要在没有带外恢复路径的情况下对远程系统应用 VRF 变更。

## 安装和验证

编辑配置后，使用仓库中的安装程序：

```sh
sudo ./scripts/install.sh --config ./gateway.yaml
systemctl status h248-sip-gateway --no-pager
journalctl -u h248-sip-gateway -f
```

预期监听地址是主路由表中的已配置 SIP 地址，以及和 VRF bind device 关联的
H.248 地址。使用 `ss -lnup` 和网关日志验证两者，不要把生产地址记录到工单
或源代码管理系统中。

防火墙只应允许：

```text
PBX_ADDRESS_SET -> SIP listener and SIP-side RTP/RTCP range
MGC_ADDRESS_SET -> H.248 listener
CARRIER_MEDIA_SET -> carrier-side RTP/RTCP range
```

允许的 RTP 范围必须包含最后一个偶数 RTP 端口之后紧邻的奇数 RTCP 端口。

## 有界探测

运行会绑定相同 H.248 源端口的探测程序前，先停止网关：

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

不要提交生成的 `gateway.yaml`、探测输出、抓包、日志、网络管理器 Profile 或
运营商配置导出文件。
