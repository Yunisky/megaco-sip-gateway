[English](README.md) | [中文](README.zh-CN.md)

# Asterisk 实验室 IP-PBX

该部署提供一个临时、可复现的 SIP PBX，用于测试 H.248 互通网关。它与运营商
VRF 有意保持分离：Asterisk 绑定 PBX 侧地址，网关继续把 H.248 信令和运营商
媒体绑定到 `vrf-h248`。

## 实验室拓扑

| 功能 | 地址 |
|---|---|
| Asterisk SIP | `192.0.2.10:5062/udp` |
| Asterisk RTP | `192.0.2.10:10000-10199/udp` |
| Gateway SIP | `192.0.2.10:5060/udp` |
| Gateway H.248 | `vrf-h248` 中的 `198.51.100.10:2944/udp` |

PBX 提供两个带认证的软电话账户 `6001` 和 `6002`。密码在测试主机上生成，
不得提交到 Git。

拨号计划：

- `6000`：包含 6001 和 6002 的振铃组；
- `6001` / `6002`：直接呼叫分机；
- `600`：Asterisk Echo 应用，用于本地音频测试；
- `9<number>`：去掉 9，把剩余号码发送到 H.248 网关；
- 从网关收到的运营商呼入：同时振铃 6001 和 6002。

## 构建和安装

实验室镜像使用 Alpine 3.20，并从 Alpine 签名软件仓库安装 Asterisk 和
PJSIP。只有环境要求使用获准的镜像仓库时，才覆盖 `BASE_IMAGE` 构建参数。

```sh
docker build \
  --file deploy/asterisk-lab/Containerfile \
  --tag h248-asterisk-lab:20 \
  deploy/asterisk-lab
```

创建只保存在主机上的凭据并安装服务：

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

只在 PBX 侧 firewalld zone 中开放端口：

```sh
firewall-cmd --permanent --zone=public --add-port=5062/udp
firewall-cmd --permanent --zone=public --add-port=10000-10199/udp
firewall-cmd --reload
```

不要把这些 PBX 端口添加到运营商 `h248` zone。

## 网关路由

在 SPES 网关 Profile 中使用以下值：

```yaml
sip:
  trunk_uri: "sip:6000@192.0.2.10:5062"
  outbound_proxy: "192.0.2.10:5062"
```

发起运营商呼叫前，先验证两个服务：

```sh
docker exec h248-asterisk-lab asterisk -rx 'core show version'
docker exec h248-asterisk-lab asterisk -rx 'pjsip show endpoints'
docker exec h248-asterisk-lab asterisk -rx 'dialplan show lab-users'
systemctl status asterisk-lab h248-sip-gateway --no-pager
```

## 软电话设置

配置 SIP 软电话：

- 服务器/域：`192.0.2.10`；
- 传输：UDP；
- 端口：`5062`；
- 用户名/认证名：`6001` 或 `6002`；
- 密码：对应的、只保存在主机上的生成密码；
- DTMF：RFC 4733/RFC 2833；
- 首选 Codec：PCMA/G.711 A-law。

在拨打外线前，先使用分机 `600` 验证本地双向音频。这个临时 PBX 不提供
匿名拨号，也不开放 AMI、ARI 或 HTTP 管理界面。

所有提交到仓库的地址都属于 RFC 5737 文档地址范围。使用前必须替换，并将
生成的凭据和运行时抓包保留在 Git 之外。
