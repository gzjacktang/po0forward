# po0forward

为少量自用 IPv4 节点编写的转发管理程序：一个 Go 面板、一个节点 Agent，以及可选的 Telegram 机器人。中文自定义界面，无外部字体、CDN 或前端框架依赖。域名和机器人参数可以后续配置。

## 工作方式

```text
业务连接：客户端 → po0 入口端口 → nftables DNAT + 定向 MASQUERADE → 落地服务器
管理连接：po0 Agent → 可选的 HTTP 代理 → HTTPS 面板
机器人：Telegram 私聊 → 面板 → 节点主动同步规则
```

面板放在 po0 对端，Agent 放在需要管理的 po0。

- 网页：添加节点、查询规则、新增、删除、启用、停用、搜索及操作记录。
- Telegram：上述规则操作和节点查询；写操作需确认，按钮 2 分钟失效；仅允许指定管理员私聊。
- 状态：区分“面板已保存”“节点已应用”；显示目标和已应用版本，回报错误、离线和恢复通知。
- 节点：独立 Token、可分别设置代理，默认先预览；保护 SSH 和已有监听端口。
- 固化：systemd 开机启动，已成功应用的快照保存到磁盘，启动时先恢复；面板不可达时仍保留并定期恢复最后成功的快照。
- nftables：预检语法后原子更新自己的 `ip pc_<节点ID>` 表，保存失败回滚该表；不清空全局防火墙。

第一版只支持 IPv4 和单个端口映射（TCP、UDP、TCP+UDP）。每台节点最多 200 条规则，面板最多 20 台节点。不提供远程任意命令、IPv6、端口范围、负载均衡或流量计费。

自定义界面减少通用面板的静态资源标志，但不能保证服务或流量无法被识别。

## 安装面板（po0 对端）

在 po0 对端服务器安装面板。不要把真实配置、密码和 Token 提交到仓库。

1. 获取对应架构的 Linux 二进制：可从仓库 GitHub Actions 的 `linux-binaries` 下载，或使用 Go 1.24 及更新版本执行 `make linux` 构建。amd64 和 arm64 的面板与 Agent 输出到 `bin/`。
2. 将源码中的 `scripts/`、`deploy/` 和对应架构的面板二进制传到对端服务器。安装脚本使用它旁边的 `../deploy/`，因此保留这两个目录的位置关系。
3. 在对端服务器初始化私有配置，记录随机密码：

```bash
chmod +x ./bin/panel-linux-amd64
./bin/panel-linux-amd64 init --config ./private/panel.json \
  --state /var/lib/po0forward-panel/state.json
bash scripts/install-panel.sh ./bin/panel-linux-amd64 ./private/panel.json
```

安装后面板只监听服务器的 `127.0.0.1:8090`，以非 root 用户 `po0panel` 运行。机器人默认关闭，域名和 HTTPS 入口按下一节配置。

## 配置域名及 HTTPS

如果对端服务器的 443 已被其他服务占用，需要单独规划 HTTPS 入口，例如独立端口、独立 IP 或现有反向代理分流。

HTTPS 反向代理应将管理域名的全部路径转发到 `127.0.0.1:8090`，包括随机的 `/sync/...`；保留请求体、`Authorization` 和 Cookie，关闭这些 API 的缓存。证书需要正常校验，不提供跳过 TLS 校验选项。

TLS 入口准备好后编辑 `/etc/po0forward/panel.json`：

```json
"public_url": "https://你的管理域名:你的HTTPS端口",
"cookie_secure": true
```

继续保持 `listen` 为 `127.0.0.1:8090`。省略默认 HTTPS 端口时不写 `:443`。随后：

```bash
systemctl restart po0forward-panel
```

改域名后用新地址重新登录。已经下载的 `agent.json` 需要同步修改 `panel_url` 并设置 `allow_local_http: false`。Agent 缓存绑定控制端地址、路径和节点 ID；已投入使用后换地址需先备份状态文件，再按新地址重新接入，不要随意删除唯一的备份。

若 Agent 通过 HTTP 代理连接面板，代理需要允许 CONNECT 到面板的实际 HTTPS 端口。使用 Tinyproxy 且只允许 443 时，使用其他端口需要调整 `ConnectPort`。代理源 IP 白名单按代理实际看到的 po0 来源设置，可能是内网地址。

## 安装 Agent（需要管理的 po0）

### 1. 在面板创建节点

为每台需要管理的 po0 添加一个节点，分别保存一次性生成的 `agent.json`。不同机器不得共用一个节点 ID 或 Token。节点 Token 丢失时当前版本需要新建节点并迁移规则，保留好原始私有配置。

设置每台的代理，例如：

```json
"proxy_url": "http://代理用户名:代理密码@代理地址:18888"
```

这是 Agent 的持续配置，不依赖 SSH 会话里的 `export http_proxy`。密码中有 `@`、`:`、`#` 等字符时需按 URL 规则百分号编码。留空时使用 Go 的系统代理环境设置；服务没有代理环境时直连。

代理中传输的是经正常校验的 HTTPS 管理连接。HTTP 代理本身的认证链路没有 TLS，是否使用 HTTPS 代理由代理服务器能力决定。

### 2. 先安装预览模式

在每台 po0 放入对应架构的 Agent 二进制、`scripts/`、`deploy/`、它自己的私有配置。默认保留 `dry_run: true`：

```bash
apt-get install nftables iproute2 procps python3
chmod +x ./bin/agent-linux-amd64
bash scripts/install-agent.sh ./bin/agent-linux-amd64 ./private/agent.json
```

安装文件：

| 文件 | 用途 |
| --- | --- |
| `/etc/po0forward/agent.json` | 独立的 Token、代理和节点配置，root 0600 |
| `/var/lib/po0forward/agent-state.json.preview.nft` | 预览模式生成的 nft 脚本 |
| `/var/lib/po0forward/agent-state.json` | 最近一次成功应用的快照 |
| `/var/lib/po0forward/agent-state.json.last-good.nft` | 最近一次更新前该表的内容，用于排查 |
| `/etc/systemd/system/po0forward-agent.service` | 开机启动与进程自动重启 |

在面板新增一条规则，查看预览：

```bash
cat /var/lib/po0forward/agent-state.json.preview.nft
journalctl -u po0forward-agent -n 30 --no-pager
```

预览不会执行规则，面板显示“应用异常：预览模式”是明确提示，而不是成功状态。预览模式也不会自动删除以前已生效的规则。

### 3. 检查已有防火墙与端口

```bash
nft list ruleset
ss -lntu
```

Agent 会拒绝占用本机监听端口、其他表可能冲突的 DNAT，或其他 `ip/inet` 表存在默认 `drop` 的 forward 链。第一版采用保守拒绝，即使这条 drop 链含有部分放行规则也需要人工检查后处理。不要为了通过预检直接清空防火墙或放开整个公网。

默认保护入口 22、22282；如果你的 SSH 使用其他端口，把它加进 `protected_ports`。内置的 po0 预设限制排除入口/目标 80、443、1080、8000、8080、8443；这不是对各服务商现行规则的保证。

目标可填能从 po0 到达的公网或内网 IPv4。不要因为有内网 IP 就假设跨厂商或跨地区可以互通。DNAT 后流量经过 forward 链，单独添加 input 放行规则不能保证业务转发可用；安全组和服务商网络限制也需要操作者检查。

### 4. 切换实际执行并固化

确认预览和网络路径后，将 `/etc/po0forward/agent.json` 中 `dry_run` 改为 `false`。用现有配置再次运行安装脚本：

```bash
bash scripts/install-agent.sh ./bin/agent-linux-amd64 /etc/po0forward/agent.json
```

实际模式安装会写入 `/etc/sysctl.d/90-po0forward.conf` 的 `net.ipv4.ip_forward = 1`，使 IPv4 转发开机保持开启。脚本不会更改现有防火墙默认策略。规则持久化由 Agent 的快照恢复负责，不要再把它生成的完整表随意追加到 `/etc/nftables.conf`，以免两套配置互相覆盖。

验证每台机器：

```bash
systemctl is-enabled po0forward-agent
systemctl is-active po0forward-agent
sysctl net.ipv4.ip_forward
journalctl -u po0forward-agent -n 30 --no-pager
```

应看到 `enabled`、`active`、转发值 `1`，并且面板中的“已应用 / 目标”版本一致、没有错误。`active` 仅说明进程运行；“已应用”说明规则已写入内核，不等于目标服务连通。最后还需从另一台机器实际连接 po0 入口验证 TCP/UDP。

Agent 启动时先恢复磁盘快照，正常运行时约每分钟核对并重建自己的表；面板中断不会主动撤销成功的规则。开机早期或防火墙重载后的恢复存在短暂间隔。停用/删除规则只阻止新的 NAT 连接，已有 conntrack 连接可能继续直到结束；程序不会清空其他业务连接。

## 配置 Telegram

向 BotFather 创建机器人，将 Token **只写入服务器私有配置**。先与自己的机器人发送 `/start`，因为 Telegram 机器人不能主动开启与用户的私聊。填写你的数字用户 ID，不使用用户名和群组 ID：

```json
"telegram_token": "后续填写BotFather提供的Token",
"telegram_admin_ids": [你的数字用户ID]
```

上面是字段说明，不是完整可直接解析的 JSON；实际 ID 填正整数。重启 `po0forward-panel` 后生效。机器人通过 HTTPS 长轮询工作，无需独立 webhook 域名。一个 Token 仅供一个正在运行的面板使用；切换旧 webhook 机器人时先移除旧 webhook。

| 命令 | 用途 |
| --- | --- |
| `/menu` | 按钮菜单 |
| `/nodes` | 查询在线、应用版本及错误 |
| `/rules` | 查询规则并提供操作按钮；每页 6 条，可翻页 |
| `/add` | 选节点，然后输入 `TCP 30001 198.51.100.20 50123 备注` |
| `/enable 规则ID` | 确认后启用 |
| `/disable 规则ID` | 确认后停用 |
| `/delete 规则ID` | 确认后删除 |
| `/cancel` | 取消当前新增输入 |

支持 `TCP`、`UDP`、`BOTH`。写操作确认绑定操作人、一次有效，并检查节点配置是否在确认期间改变。未授权用户和群组消息不会修改规则。通知是尽力投递，不是可靠消息队列；Telegram 或网络故障时以网页状态和日志为准。

## 备份、升级和恢复

- 面板私有配置：`/etc/po0forward/panel.json`；面板状态：`/var/lib/po0forward-panel/state.json`，包含节点凭据哈希、规则和最近 300 条操作记录。
- 每台 Agent 各自备份 `/etc/po0forward/agent.json` 与 `/var/lib/po0forward/agent-state.json`。
- 升级先备份，再运行安装脚本并传入现有 `/etc/po0forward/*.json`，不会自动覆盖凭据。备份节点配置与面板状态必须配套。
- 面板重启会清除登录会话；在线状态等待下一次心跳刷新。规则保存在磁盘。
- 面板从过旧备份恢复时，Agent 拒绝版本倒退，避免静默覆盖新规则。恢复正确备份或经人工核对后迁移。
- 停止 Agent 服务不会删除已在内核的规则。需要停转发时先在面板停用并等待节点确认，再停服务；面板不可达时应先人工检查该节点自己的表再处理。

管理员密码丢失时，在面板服务器生成新随机密码并重启：

```bash
po0forward-panel password --config /etc/po0forward/panel.json
chown root:po0panel /etc/po0forward/panel.json
chmod 0640 /etc/po0forward/panel.json
systemctl restart po0forward-panel
```

务必保管新密码。原始节点 Token 只保存在操作者下载的配置中，面板不能恢复明文 Token。
