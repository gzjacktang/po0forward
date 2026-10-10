#!/usr/bin/env bash
# Run only on a disposable GitHub Actions Linux runner, never on a real VPS.
set -euo pipefail
[[ $EUID -eq 0 && ${GITHUB_ACTIONS:-} == true ]] || { echo '仅用于 GitHub Actions 临时 Linux runner'; exit 1; }
[[ $# -eq 2 ]] || { echo "用法: $0 面板二进制 Agent二进制"; exit 1; }
[[ $(ps -p 1 -o comm=) == systemd ]] || { echo '测试需要 systemd'; exit 1; }
task_panel=$(realpath "$1")
task_agent=$(realpath "$2")
task_root=$(cd -- "$(dirname -- "$0")/.." && pwd)
# Abort before cleanup is registered if anything already belongs to another install.
for task_path in /etc/po0forward /var/lib/po0forward /var/lib/po0forward-panel /etc/systemd/system/po0forward-panel.service /etc/systemd/system/po0forward-agent.service /usr/local/bin/po0forward-panel /usr/local/bin/po0forward-agent; do
  [[ ! -e $task_path ]] || { echo '已有安装，拒绝在此系统运行测试'; exit 1; }
done
task_tmp=$(mktemp -d)
cleanup() {
  systemctl disable --now po0forward-agent po0forward-panel >/dev/null 2>&1 || true
  rm -f /etc/systemd/system/po0forward-panel.service /etc/systemd/system/po0forward-agent.service
  rm -f /usr/local/bin/po0forward-panel /usr/local/bin/po0forward-agent
  rm -rf /etc/po0forward /var/lib/po0forward /var/lib/po0forward-panel "$task_tmp"
  systemctl daemon-reload
}
trap cleanup EXIT
"$task_panel" init --config "$task_tmp/panel.json" --state /var/lib/po0forward-panel/state.json > "$task_tmp/bootstrap"
bash "$task_root/scripts/install-panel.sh" "$task_panel" "$task_tmp/panel.json"
python3 - "$task_tmp" <<'PY'
import json,time,sys,urllib.request,http.cookiejar
from pathlib import Path
p=Path(sys.argv[1]);password=next(line.split(': ',1)[1] for line in (p/'bootstrap').read_text().splitlines() if line.startswith('Admin password: '))
jar=http.cookiejar.CookieJar()
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(jar))
csrf=''
def post(path,data):
    r=urllib.request.Request('http://127.0.0.1:8090/api/'+path,data=json.dumps(data).encode(),headers={'Content-Type':'application/json','X-CSRF-Token':csrf})
    with client.open(r,timeout=3) as response: return json.load(response)
for attempt in range(30):
    try:
        csrf=post('login',{'password':password})['csrf'];break
    except OSError:
        if attempt==29: raise
        time.sleep(.2)
enrollment=post('nodes',{'name':'installation smoke test'})
config=enrollment['agent_config']
assert config['dry_run'] is True
(p/'agent.json').write_text(json.dumps(config));(p/'agent.json').chmod(0o600)
post('rules',dict(node_id=config['node_id'],name='preview only',protocol='tcp',listen_port=30001,target_ip='198.51.100.20',target_port=50123,enabled=True))
PY
bash "$task_root/scripts/install-agent.sh" "$task_agent" "$task_tmp/agent.json"
task_ready=no
for ((task_i=0; task_i<30; task_i++)); do
  if [[ -s /var/lib/po0forward/agent-state.json.preview.nft ]]; then task_ready=yes; break; fi
  sleep .2
done
[[ $task_ready == yes ]] || { journalctl -u po0forward-agent -n 20 --no-pager; exit 1; }
[[ ! -e /var/lib/po0forward/agent-state.json ]] || { echo '预览模式意外提交了实际快照'; exit 1; }
systemctl is-enabled po0forward-panel po0forward-agent
systemctl is-active po0forward-panel po0forward-agent
[[ $(stat -c '%U' /var/lib/po0forward-panel/state.json) == po0panel ]]
[[ $(stat -c '%a' /etc/po0forward/agent.json) == 600 ]]
# Agent installation must preserve the panel's ability to read its configuration.
systemctl restart po0forward-panel
sleep .5
systemctl is-active po0forward-panel
echo 'PASS: 非 root 面板、私有配置权限、systemd 启动、Agent 预览及同机安装隔离'
