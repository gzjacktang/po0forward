package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gzjacktang/po0forward/internal/model"
	"github.com/gzjacktang/po0forward/internal/storage"
)

type Config struct {
	PanelURL       string `json:"panel_url"`
	ControlPath    string `json:"control_path"`
	NodeID         string `json:"node_id"`
	Token          string `json:"token"`
	ProxyURL       string `json:"proxy_url"`
	TableName      string `json:"table_name"`
	StateFile      string `json:"state_file"`
	ProtectedPorts []int  `json:"protected_ports"`
	DryRun         bool   `json:"dry_run"`
	AllowLocalHTTP bool   `json:"allow_local_http"`
}

// Missing dry_run must never turn a downloaded or hand-edited configuration
// into a live firewall update by accident.
func (c *Config) UnmarshalJSON(data []byte) error {
	type plainConfig Config
	*c = Config{DryRun: true}
	return json.Unmarshal(data, (*plainConfig)(c))
}

func (c Config) Validate() error {
	u, err := url.Parse(c.PanelURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return errors.New("panel_url 格式错误")
	}
	if u.Scheme != "https" && !(c.AllowLocalHTTP && u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return errors.New("节点控制连接需要 HTTPS；HTTP 仅允许本机开发")
	}
	if !regexp.MustCompile(`^/sync/[a-f0-9]{24}$`).MatchString(c.ControlPath) || !model.IDPattern.MatchString(c.NodeID) || len(c.Token) != 64 || c.TableName != "pc_"+c.NodeID || c.StateFile == "" {
		return errors.New("节点配置不完整或 nft 表名与节点 ID 不匹配")
	}
	if _, err = hexToken(c.Token); err != nil {
		return err
	}
	if c.ProxyURL != "" {
		p, e := url.Parse(c.ProxyURL)
		if e != nil || p.Host == "" || (p.Scheme != "http" && p.Scheme != "https") {
			return errors.New("proxy_url 需要合法的 HTTP 代理 URL")
		}
	}
	return nil
}
func hexToken(token string) (bool, error) {
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(token) {
		return false, errors.New("invalid node token")
	}
	return true, nil
}

type Saved struct {
	Controller string         `json:"controller"`
	Snapshot   model.Snapshot `json:"snapshot"`
}
type Runner func(context.Context, string, []string, string) ([]byte, error)

func Execute(ctx context.Context, name string, args []string, input string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 1800 {
			out = out[:1800]
		}
		return out, fmt.Errorf("%s: %s", name, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type Engine struct {
	Config Config
	Run    Runner
	Commit func(Saved) error
}

func NewEngine(c Config) *Engine {
	return &Engine{Config: c, Run: Execute, Commit: func(s Saved) error { return storage.WriteJSON(c.StateFile, s) }}
}
func (e *Engine) Identity() string {
	return model.Hash(e.Config.PanelURL + e.Config.ControlPath + e.Config.NodeID)
}
func Render(table string, s model.Snapshot, exists bool) (string, error) {
	if table != "pc_"+s.NodeID {
		return "", errors.New("unsafe nft table name")
	}
	if err := model.ValidateSnapshot(s, nil); err != nil {
		return "", err
	}
	var b strings.Builder
	if exists {
		fmt.Fprintf(&b, "delete table ip %s\n", table)
	}
	fmt.Fprintf(&b, "table ip %s {\n chain prerouting {\n  type nat hook prerouting priority dstnat; policy accept;\n", table)
	for _, r := range s.Rules {
		if r.Enabled {
			for _, p := range model.Protocols(r.Protocol) {
				fmt.Fprintf(&b, "  iifname != \"lo\" %s dport %d counter dnat to %s:%d comment %s\n", p, r.ListenPort, r.TargetIP, r.TargetPort, strconv.Quote(r.ID+":"+p))
			}
		}
	}
	b.WriteString(" }\n chain postrouting {\n  type nat hook postrouting priority srcnat; policy accept;\n")
	for _, r := range s.Rules {
		if r.Enabled {
			for _, p := range model.Protocols(r.Protocol) {
				fmt.Fprintf(&b, "  ip daddr %s %s dport %d ct original proto-dst %d ct status dnat masquerade comment %s\n", r.TargetIP, p, r.TargetPort, r.ListenPort, strconv.Quote(r.ID+":"+p))
			}
		}
	}
	b.WriteString(" }\n}\n")
	return b.String(), nil
}
func hasDNAT(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "dnat" {
				return true
			}
			if k == "xt" {
				if m, ok := val.(map[string]any); ok && m["name"] == "DNAT" {
					return true
				}
			}
			if hasDNAT(val) {
				return true
			}
		}
	case []any:
		for _, val := range x {
			if hasDNAT(val) {
				return true
			}
		}
	}
	return false
}
func portIncludes(v any, port int) bool {
	switch x := v.(type) {
	case float64:
		return int(x) == port
	case map[string]any:
		if r, ok := x["range"].([]any); ok && len(r) == 2 {
			lo, l := r[0].(float64)
			hi, h := r[1].(float64)
			if l && h {
				return port >= int(lo) && port <= int(hi)
			}
		}
		if set, ok := x["set"]; ok {
			return portIncludes(set, port)
		}
	case []any:
		for _, elem := range x {
			if portIncludes(elem, port) {
				return true
			}
		}
	}
	return false
}
func destinationPortMatch(v any, port int) (found, conflict bool) {
	switch x := v.(type) {
	case map[string]any:
		if m, ok := x["match"].(map[string]any); ok {
			left, _ := m["left"].(map[string]any)
			payload, _ := left["payload"].(map[string]any)
			if payload["field"] == "dport" {
				// Unknown set references, operators and payload protocols are
				// conservatively treated as a possible collision.
				operator, _ := m["op"].(string)
				protocol, _ := payload["protocol"].(string)
				if !knownPorts(m["right"]) || (protocol != "tcp" && protocol != "udp") {
					return true, true
				}
				match := portIncludes(m["right"], port)
				switch operator {
				case "", "==", "in":
					return true, match
				case "!=":
					return true, !match
				default:
					return true, true
				}
			}
		}
		for _, val := range x {
			f, c := destinationPortMatch(val, port)
			found = found || f
			conflict = conflict || c
		}
	case []any:
		for _, val := range x {
			f, c := destinationPortMatch(val, port)
			found = found || f
			conflict = conflict || c
		}
	}
	return
}

func knownPorts(v any) bool {
	switch x := v.(type) {
	case float64:
		return x >= 0 && x <= 65535 && x == float64(int(x))
	case map[string]any:
		if r, ok := x["range"].([]any); ok && len(r) == 2 {
			return knownPorts(r[0]) && knownPorts(r[1])
		}
		if set, ok := x["set"]; ok {
			return knownPorts(set)
		}
	case []any:
		if len(x) == 0 {
			return false
		}
		for _, item := range x {
			if !knownPorts(item) {
				return false
			}
		}
		return true
	}
	return false
}

// Preflight never edits another application's tables. It rejects conflicting
// DNAT and a drop-policy forward chain rather than pretending NAT can override it.
func Preflight(raw []byte, table string, s model.Snapshot) (bool, error) {
	var doc struct {
		Objects []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false, errors.New("无法解析 nftables 状态")
	}
	exists := false
	enabled := false
	for _, r := range s.Rules {
		enabled = enabled || r.Enabled
	}
	for _, obj := range doc.Objects {
		if b, ok := obj["table"]; ok {
			var t struct{ Family, Name string }
			_ = json.Unmarshal(b, &t)
			if t.Family == "ip" && t.Name == table {
				exists = true
			}
		}
		if b, ok := obj["chain"]; ok && enabled {
			var c struct{ Family, Table, Hook, Policy string }
			_ = json.Unmarshal(b, &c)
			if (c.Family == "ip" || c.Family == "inet") && c.Table != table && c.Hook == "forward" && c.Policy == "drop" {
				return false, fmt.Errorf("已有 forward 默认拒绝策略（%s），需先放行转发流量", c.Table)
			}
		}
		if b, ok := obj["rule"]; ok && enabled {
			var r struct {
				Family, Table string
				Expr          any
			}
			_ = json.Unmarshal(b, &r)
			if (r.Family != "ip" && r.Family != "inet") || r.Table == table || !hasDNAT(r.Expr) {
				continue
			}
			for _, rule := range s.Rules {
				if rule.Enabled {
					found, conflict := destinationPortMatch(r.Expr, rule.ListenPort)
					if conflict || !found {
						return false, fmt.Errorf("已有 NAT 规则可能占用入口 %d（表 %s），请先检查或迁移", rule.ListenPort, r.Table)
					}
				}
			}
		}
	}
	return exists, nil
}
func (e *Engine) Apply(ctx context.Context, s model.Snapshot) error {
	if s.NodeID != e.Config.NodeID {
		return errors.New("节点 ID 不匹配")
	}
	if err := model.ValidateSnapshot(s, e.Config.ProtectedPorts); err != nil {
		return err
	}
	if e.Config.DryRun {
		script, err := Render(e.Config.TableName, s, false)
		if err != nil {
			return err
		}
		if err = storage.WriteFile(e.Config.StateFile+".preview.nft", []byte(script)); err != nil {
			return err
		}
		return errors.New("预览模式：规则未执行；检查 preview.nft 后将 dry_run 改为 false")
	}
	raw, err := e.Run(ctx, "nft", []string{"-j", "list", "ruleset"}, "")
	if err != nil {
		return err
	}
	exists, err := Preflight(raw, e.Config.TableName, s)
	if err != nil {
		return err
	}
	listeners, err := e.Run(ctx, "ss", []string{"-H", "-lntu"}, "")
	if err != nil {
		return err
	}
	if err = ListenerConflict(string(listeners), s); err != nil {
		return err
	}
	forwarding, err := e.Run(ctx, "sysctl", []string{"-n", "net.ipv4.ip_forward"}, "")
	if err != nil {
		return err
	}
	for _, rule := range s.Rules {
		if rule.Enabled && strings.TrimSpace(string(forwarding)) != "1" {
			return errors.New("IPv4 转发未开启，请检查安装脚本的 sysctl 配置")
		}
	}
	script, err := Render(e.Config.TableName, s, exists)
	if err != nil {
		return err
	}
	if _, err = e.Run(ctx, "nft", []string{"-c", "-f", "-"}, script); err != nil {
		return fmt.Errorf("规则语法验证失败: %w", err)
	}
	old := []byte{}
	if exists {
		old, err = e.Run(ctx, "nft", []string{"-s", "list", "table", "ip", e.Config.TableName}, "")
		if err != nil {
			return err
		}
	}
	if err = storage.WriteFile(e.Config.StateFile+".last-good.nft", old); err != nil {
		return err
	}
	if _, err = e.Run(ctx, "nft", []string{"-f", "-"}, script); err != nil {
		return fmt.Errorf("原子应用失败，保留旧规则: %w", err)
	}
	if err = e.Commit(Saved{Controller: e.Identity(), Snapshot: s}); err != nil {
		rollback := "delete table ip " + e.Config.TableName + "\n" + string(old)
		_, rollbackErr := e.Run(ctx, "nft", []string{"-f", "-"}, rollback)
		if rollbackErr != nil {
			return fmt.Errorf("配置保存失败且回滚失败，需人工检查: %v; %v", err, rollbackErr)
		}
		return fmt.Errorf("配置保存失败，已恢复原规则: %w", err)
	}
	return nil
}

func ListenerConflict(output string, snapshot model.Snapshot) error {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		local := fields[4]
		pos := strings.LastIndex(local, ":")
		if pos < 0 {
			continue
		}
		port, err := strconv.Atoi(local[pos+1:])
		if err != nil {
			continue
		}
		protocol := fields[0]
		for _, rule := range snapshot.Rules {
			if !rule.Enabled || rule.ListenPort != port {
				continue
			}
			for _, p := range model.Protocols(rule.Protocol) {
				if strings.HasPrefix(protocol, p) {
					return fmt.Errorf("入口端口 %d/%s 已被本机服务监听", port, p)
				}
			}
		}
	}
	return nil
}
func Client(c Config) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 4
	transport.MaxConnsPerHost = 4
	if c.ProxyURL != "" {
		p, err := url.Parse(c.ProxyURL)
		if err != nil {
			return nil, errors.New("invalid proxy")
		}
		transport.Proxy = http.ProxyURL(p)
	}
	return &http.Client{Timeout: 12 * time.Second, Transport: transport}, nil
}
func Poll(ctx context.Context, client *http.Client, c Config, h model.Heartbeat) (model.Snapshot, error) {
	b, _ := json.Marshal(h)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.PanelURL+c.ControlPath, bytes.NewReader(b))
	if err != nil {
		return model.Snapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "node-client")
	response, err := client.Do(req)
	if err != nil {
		return model.Snapshot{}, errors.New("控制连接失败，请检查 TLS、代理凭据及网络")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return model.Snapshot{}, fmt.Errorf("控制端返回 HTTP %d", response.StatusCode)
	}
	var snapshot model.Snapshot
	err = json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&snapshot)
	return snapshot, err
}
func Run(ctx context.Context, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	client, err := Client(c)
	if err != nil {
		return err
	}
	engine := NewEngine(c)
	h := model.Heartbeat{NodeID: c.NodeID, Version: model.Version}
	lastApply := time.Time{}
	var saved Saved
	if err = storage.ReadJSON(c.StateFile, &saved); err == nil {
		if saved.Controller != engine.Identity() {
			return errors.New("本地规则属于其他控制端，请先备份并迁移 state_file")
		}
		h.AttemptRevision = saved.Snapshot.Revision
		if err = engine.Apply(ctx, saved.Snapshot); err != nil {
			h.Error = err.Error()
		} else {
			h.AppliedRevision = saved.Snapshot.Revision
			lastApply = time.Now()
			log.Printf("restored revision %d", h.AppliedRevision)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var retryAt time.Time
	var lastError string
	for {
		// Restore/reconcile the last committed snapshot even when the control
		// connection is unavailable (for example after a firewall reload).
		if saved.Snapshot.Revision > 0 && time.Now().After(retryAt) && time.Since(lastApply) >= time.Minute {
			h.AttemptRevision = saved.Snapshot.Revision
			if err = engine.Apply(ctx, saved.Snapshot); err != nil {
				h.Error = err.Error()
				if len(h.Error) > 900 {
					h.Error = h.Error[:900]
				}
				retryAt = time.Now().Add(30 * time.Second)
				log.Print(h.Error)
			} else {
				h.AppliedRevision = saved.Snapshot.Revision
				h.Error = ""
				lastApply = time.Now()
			}
		}
		snapshot, pollErr := Poll(ctx, client, c, h)
		if pollErr != nil {
			if pollErr.Error() != lastError {
				log.Print(pollErr)
				lastError = pollErr.Error()
			}
		} else {
			lastError = ""
			if snapshot.Revision < h.AppliedRevision {
				return errors.New("控制端版本倒退，拒绝覆盖节点现有规则；请先恢复面板备份")
			}
			if time.Now().After(retryAt) && (snapshot.Revision != h.AppliedRevision || time.Since(lastApply) >= time.Minute) {
				h.AttemptRevision = snapshot.Revision
				if err = engine.Apply(ctx, snapshot); err != nil {
					h.Error = err.Error()
					if len(h.Error) > 900 {
						h.Error = h.Error[:900]
					}
					retryAt = time.Now().Add(30 * time.Second)
					log.Print(h.Error)
				} else {
					saved = Saved{Controller: engine.Identity(), Snapshot: snapshot}
					h.AppliedRevision = snapshot.Revision
					h.Error = ""
					lastApply = time.Now()
					retryAt = time.Time{}
					log.Printf("applied revision %d", snapshot.Revision)
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
