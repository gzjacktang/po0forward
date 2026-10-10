#!/usr/bin/env bash
set -euo pipefail
[[ $EUID -eq 0 ]] || { echo '请使用 root 执行'; exit 1; }
[[ $# -eq 2 ]] || { echo "用法: $0 Agent二进制 私有agent.json"; exit 1; }
command -v systemctl >/dev/null
command -v python3 >/dev/null
task_binary=$(realpath "$1")
task_config=$(realpath "$2")
task_root=$(cd -- "$(dirname -- "$0")/.." && pwd)
[[ -x $task_binary && -f $task_config ]]
"$task_binary" validate --config "$task_config"
python3 - "$task_config" <<'PY'
import json,sys
c=json.load(open(sys.argv[1]))
if c.get('state_file') != '/var/lib/po0forward/agent-state.json':
    sys.exit('安装版要求 state_file=/var/lib/po0forward/agent-state.json')
if 'dry_run' not in c or not isinstance(c['dry_run'], bool):
    sys.exit('请明确填写 dry_run: true（预览）或 false（实际执行）')
PY
for task_command in nft ss sysctl; do
  command -v "$task_command" >/dev/null || { echo '先安装依赖: apt-get install nftables iproute2 procps'; exit 1; }
done
[[ -d /etc/po0forward ]] || install -d -m 0750 /etc/po0forward
install -d -m 0700 /var/lib/po0forward
if [[ -e /etc/po0forward/agent.json && $task_config != /etc/po0forward/agent.json ]]; then
  echo '已有 /etc/po0forward/agent.json，请先备份；若为升级，传入这个现有文件。'
  exit 1
fi
if [[ $task_config != /etc/po0forward/agent.json ]]; then
  install -m 0600 -o root -g root "$task_config" /etc/po0forward/agent.json
fi
chown root:root /etc/po0forward/agent.json
chmod 0600 /etc/po0forward/agent.json
install -m 0755 "$task_binary" /usr/local/bin/po0forward-agent
install -m 0644 "$task_root/deploy/po0forward-agent.service" /etc/systemd/system/po0forward-agent.service
# Enable forwarding only when the operator has explicitly selected live mode.
task_live=$(python3 -c 'import json,sys; print("yes" if json.load(open(sys.argv[1]))["dry_run"] is False else "no")' /etc/po0forward/agent.json)
if [[ $task_live == yes ]]; then
  install -d -m 0755 /etc/sysctl.d
  printf '%s\n' 'net.ipv4.ip_forward = 1' > /etc/sysctl.d/90-po0forward.conf
  chmod 0644 /etc/sysctl.d/90-po0forward.conf
  sysctl -w net.ipv4.ip_forward=1
fi
systemctl daemon-reload
systemctl enable po0forward-agent
systemctl restart po0forward-agent
systemctl is-active po0forward-agent
echo '已安装。请同时查看面板的“已应用 / 目标”版本和 Agent 日志，active 不等于规则应用成功。'
