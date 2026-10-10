#!/usr/bin/env bash
# Real nftables/TCP/UDP tests confined to three temporary network namespaces.
# No change to the host's firewall, routes, forwarding sysctl or service units.
set -euo pipefail
[[ $EUID -eq 0 && $(uname -s) == Linux ]] || { echo '此测试需要 Linux root 和网络命名空间权限'; exit 1; }
[[ $# -eq 1 ]] || { echo "用法: $0 Agent二进制"; exit 1; }
for task_command in nft ip ss sysctl python3; do command -v "$task_command" >/dev/null; done
task_binary=$(realpath "$1")
task_tmp=$(mktemp -d)
task_tag="pf-${BASHPID}"
task_client="${task_tag}-c"
task_node="${task_tag}-n"
task_backend="${task_tag}-b"
task_spaces=()
cleanup() {
  for task_ns in "${task_spaces[@]}"; do
    while read -r task_pid; do
      [[ -z $task_pid ]] || kill "$task_pid" 2>/dev/null || true
    done < <(ip netns pids "$task_ns" 2>/dev/null)
  done
  for task_ns in "${task_spaces[@]}"; do ip netns del "$task_ns" 2>/dev/null || true; done
  rm -rf "$task_tmp"
}
trap cleanup EXIT
for task_ns in "$task_client" "$task_node" "$task_backend"; do
  ip netns add "$task_ns"
  task_spaces+=("$task_ns")
  ip -n "$task_ns" link set lo up
done
ip -n "$task_node" link add front type veth peer name client netns "$task_client"
ip -n "$task_node" link add back type veth peer name backend netns "$task_backend"
ip -n "$task_node" addr add 192.0.2.1/24 dev front
ip -n "$task_client" addr add 192.0.2.2/24 dev client
ip -n "$task_node" addr add 198.51.100.1/24 dev back
ip -n "$task_backend" addr add 198.51.100.2/24 dev backend
ip -n "$task_node" link set front up
ip -n "$task_node" link set back up
ip -n "$task_client" link set client up
ip -n "$task_backend" link set backend up
ip -n "$task_client" route add default via 192.0.2.1
# The backend has no route to the client subnet: replies require scoped SNAT.
ip netns exec "$task_node" sysctl -qw net.ipv4.ip_forward=1
ip netns exec "$task_node" nft 'add table ip unrelated'
ip netns exec "$task_node" nft 'add chain ip unrelated marker'
python3 - "$task_tmp" <<'PY'
import json,sys
from pathlib import Path
p=Path(sys.argv[1]); node='0011223344556677'
config=dict(panel_url='https://192.0.2.254',control_path='/sync/'+'1'*24,node_id=node,
 token='1'*64,proxy_url='',table_name='pc_'+node,state_file=str(p/'state.json'),
 protected_ports=[22,22282],dry_run=False,allow_local_http=False)
rule=dict(id='1122334455667788',node_id=node,name='integration',protocol='both',
 listen_port=30123,target_ip='198.51.100.2',target_port=40123,enabled=True)
snap=dict(node_id=node,revision=1,rules=[rule])
(p/'config.json').write_text(json.dumps(config))
(p/'enabled.json').write_text(json.dumps(snap))
snap['revision']=2;rule['enabled']=False
(p/'disabled.json').write_text(json.dumps(snap))
rule['enabled']=True;rule['target_ip']='198.51.100.2; flush ruleset'
(p/'invalid.json').write_text(json.dumps(snap))
PY
cat > "$task_tmp/echo.py" <<'PY'
import socket,threading
def tcp():
    s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
    s.bind(('198.51.100.2',40123));s.listen()
    while True:
        c,_=s.accept()
        with c: c.sendall(c.recv(128))
def udp():
    s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(('198.51.100.2',40123))
    while True:
        data,peer=s.recvfrom(128);s.sendto(data,peer)
threading.Thread(target=tcp,daemon=True).start()
threading.Thread(target=udp,daemon=True).start()
threading.Event().wait()
PY
cat > "$task_tmp/client.py" <<'PY'
import socket,sys
expect=sys.argv[1]=='open'
for protocol,kind in [('TCP',socket.SOCK_STREAM),('UDP',socket.SOCK_DGRAM)]:
    s=socket.socket(socket.AF_INET,kind);s.settimeout(1)
    succeeded=False
    try:
        s.connect(('192.0.2.1',30123));s.send(b'forward-check')
        succeeded=s.recv(128)==b'forward-check'
    except OSError: pass
    finally: s.close()
    if succeeded!=expect: raise SystemExit(protocol+' forwarding state incorrect')
    print(protocol+': '+sys.argv[1])
PY
ip netns exec "$task_backend" python3 "$task_tmp/echo.py" > "$task_tmp/echo.log" 2>&1 &
for ((task_i=0; task_i<30; task_i++)); do
  if ip netns exec "$task_backend" python3 -c 'import socket;s=socket.socket();s.settimeout(.1);s.connect(("198.51.100.2",40123));s.close()' 2>/dev/null; then break; fi
  sleep .1
done
ip netns exec "$task_node" "$task_binary" apply --config "$task_tmp/config.json" --snapshot "$task_tmp/enabled.json"
ip netns exec "$task_client" python3 "$task_tmp/client.py" open
if ip netns exec "$task_node" "$task_binary" apply --config "$task_tmp/config.json" --snapshot "$task_tmp/invalid.json" > "$task_tmp/invalid.log" 2>&1; then
  echo '非法快照意外被接受'; exit 1
fi
ip netns exec "$task_client" python3 "$task_tmp/client.py" open
# An existing listener must reject a conflicting rule without replacing good rules.
ip netns exec "$task_node" python3 -c 'import socket,time;s=socket.socket();s.bind(("0.0.0.0",30223));s.listen();time.sleep(60)' &
task_listener=$!
sleep .3
python3 - "$task_tmp" <<'PY'
import json,sys
from pathlib import Path
p=Path(sys.argv[1]);s=json.loads((p/'enabled.json').read_text())
s['revision']=2;s['rules'][0]['listen_port']=30223
(p/'conflict.json').write_text(json.dumps(s))
PY
if ip netns exec "$task_node" "$task_binary" apply --config "$task_tmp/config.json" --snapshot "$task_tmp/conflict.json" > "$task_tmp/conflict.log" 2>&1; then
  echo '监听端口冲突意外被接受'; exit 1
fi
kill "$task_listener"
ip netns exec "$task_client" python3 "$task_tmp/client.py" open
# Simulate reboot/firewall reload and restore without a reachable controller.
ip netns exec "$task_node" nft delete table ip pc_0011223344556677
ip netns exec "$task_node" "$task_binary" --config "$task_tmp/config.json" > "$task_tmp/agent.log" 2>&1 &
task_agent=$!
task_restored=no
for ((task_i=0; task_i<40; task_i++)); do
  if ip netns exec "$task_node" nft list table ip pc_0011223344556677 >/dev/null 2>&1; then task_restored=yes; break; fi
  sleep .1
done
[[ $task_restored == yes ]] || { cat "$task_tmp/agent.log"; exit 1; }
ip netns exec "$task_client" python3 "$task_tmp/client.py" open
kill "$task_agent"
wait "$task_agent" || true
ip netns exec "$task_node" "$task_binary" apply --config "$task_tmp/config.json" --snapshot "$task_tmp/disabled.json"
ip netns exec "$task_client" python3 "$task_tmp/client.py" closed
ip netns exec "$task_node" nft list chain ip unrelated marker >/dev/null
python3 - "$task_tmp/state.json" <<'PY'
import json,sys
assert json.load(open(sys.argv[1]))['snapshot']['revision']==2
PY
echo 'PASS: TCP/UDP NAT、预检失败保留旧规则、离线启动恢复、停用和其他表保护'
