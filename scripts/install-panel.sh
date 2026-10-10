#!/usr/bin/env bash
set -euo pipefail
[[ $EUID -eq 0 ]] || { echo '请使用 root 执行'; exit 1; }
[[ $# -eq 2 ]] || { echo "用法: $0 面板二进制 私有panel.json"; exit 1; }
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
if c.get('state_file') != '/var/lib/po0forward-panel/state.json':
    sys.exit('安装版要求 state_file=/var/lib/po0forward-panel/state.json')
if c.get('listen') != '127.0.0.1:8090':
    sys.exit('安装版只监听 127.0.0.1:8090；公网入口请后续通过 HTTPS 反代配置')
PY
getent group po0panel >/dev/null || groupadd --system po0panel
id po0panel >/dev/null 2>&1 || useradd --system --gid po0panel --home-dir /nonexistent --shell /usr/sbin/nologin po0panel
install -d -m 0750 -o root -g po0panel /etc/po0forward
install -d -m 0700 -o po0panel -g po0panel /var/lib/po0forward-panel
# Preserve an existing private configuration rather than overwriting credentials.
if [[ -e /etc/po0forward/panel.json && $task_config != /etc/po0forward/panel.json ]]; then
  echo '已有 /etc/po0forward/panel.json，请先备份；若为升级，传入这个现有文件。'
  exit 1
fi
if [[ $task_config != /etc/po0forward/panel.json ]]; then
  install -m 0640 -o root -g po0panel "$task_config" /etc/po0forward/panel.json
fi
chown root:po0panel /etc/po0forward/panel.json
chmod 0640 /etc/po0forward/panel.json
install -m 0755 "$task_binary" /usr/local/bin/po0forward-panel
install -m 0644 "$task_root/deploy/po0forward-panel.service" /etc/systemd/system/po0forward-panel.service
systemctl daemon-reload
systemctl enable po0forward-panel
systemctl restart po0forward-panel
systemctl is-active po0forward-panel
echo '已安装。面板仅监听本机；域名及 Telegram 可后续编辑 panel.json。'
