package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gzjacktang/po0forward/internal/model"
)

func TestMissingDryRunCannotEnableLiveUpdates(t *testing.T) {
	c, _ := fixture(t)
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "dry_run")
	raw, _ = json.Marshal(fields)
	var decoded Config
	if err := json.Unmarshal(raw, &decoded); err != nil || !decoded.DryRun {
		t.Fatal("omitted mode enabled live updates")
	}
	engine := NewEngine(decoded)
	called := false
	engine.Run = func(context.Context, string, []string, string) ([]byte, error) {
		called = true
		return nil, nil
	}
	_, snapshot := fixture(t)
	if err := engine.Apply(context.Background(), snapshot); err == nil || called {
		t.Fatal("preview mode executed a firewall command")
	}
}

func fixture(t *testing.T) (Config, model.Snapshot) {
	t.Helper()
	id := "0011223344556677"
	c := Config{PanelURL: "https://example.invalid", ControlPath: "/sync/" + strings.Repeat("1", 24), NodeID: id, Token: strings.Repeat("1", 64), TableName: "pc_" + id, StateFile: filepath.Join(t.TempDir(), "state.json")}
	return c, model.Snapshot{NodeID: id, Revision: 2, Rules: []model.Rule{{ID: "1122334455667788", NodeID: id, Name: "route", Protocol: "both", ListenPort: 30001, TargetIP: "198.51.100.20", TargetPort: 50123, Enabled: true}}}
}
func fakeEngine(t *testing.T) (*Engine, model.Snapshot, *[]string) {
	c, s := fixture(t)
	scripts := []string{}
	e := NewEngine(c)
	e.Run = func(_ context.Context, name string, args []string, input string) ([]byte, error) {
		if name == "sysctl" {
			return []byte("1\n"), nil
		}
		if name == "ss" {
			return []byte{}, nil
		}
		switch strings.Join(args, " ") {
		case "-j list ruleset":
			return []byte(`{"nftables":[{"table":{"family":"ip","name":"` + c.TableName + `"}}]}`), nil
		case "-s list table ip " + c.TableName:
			return []byte("table ip " + c.TableName + " { chain old { } }\n"), nil
		case "-f -":
			scripts = append(scripts, input)
		}
		return nil, nil
	}
	return e, s, &scripts
}
func TestCommitFailureRollsBackOnlyOwnedTable(t *testing.T) {
	e, s, scripts := fakeEngine(t)
	e.Commit = func(Saved) error { return errors.New("disk full") }
	err := e.Apply(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "已恢复") {
		t.Fatal("commit failure not rolled back")
	}
	if len(*scripts) != 2 || !strings.Contains((*scripts)[1], "chain old") {
		t.Fatal("exact previous table not restored")
	}
	for _, script := range *scripts {
		if strings.Contains(script, "flush ruleset") || strings.Contains(script, "delete table ip unrelated") {
			t.Fatal("another firewall table changed")
		}
	}
}
func TestCheckFailureNeverAppliesOrCommits(t *testing.T) {
	e, s, scripts := fakeEngine(t)
	original := e.Run
	e.Run = func(ctx context.Context, name string, args []string, input string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"-c", "-f", "-"}) {
			return nil, errors.New("syntax")
		}
		return original(ctx, name, args, input)
	}
	committed := false
	e.Commit = func(Saved) error { committed = true; return nil }
	if e.Apply(context.Background(), s) == nil || committed || len(*scripts) != 0 {
		t.Fatal("invalid batch mutated runtime or cache")
	}
}
func TestExternalDNATConflictAndForwardDrop(t *testing.T) {
	c, s := fixture(t)
	collision := []byte(`{"nftables":[{"rule":{"family":"ip","table":"other","expr":[{"match":{"left":{"payload":{"protocol":"tcp","field":"dport"}},"right":{"range":[30000,30100]}}},{"dnat":{"addr":"198.51.100.10"}}]}}]}`)
	if _, err := Preflight(collision, c.TableName, s); err == nil {
		t.Fatal("external port-range DNAT conflict ignored")
	}
	drop := []byte(`{"nftables":[{"chain":{"family":"inet","table":"ufw","hook":"forward","policy":"drop"}}]}`)
	if _, err := Preflight(drop, c.TableName, s); err == nil {
		t.Fatal("forward drop policy ignored")
	}
	if err := ListenerConflict("tcp LISTEN 0 128 0.0.0.0:30001 0.0.0.0:*", s); err == nil {
		t.Fatal("existing service listener ignored")
	}
}
func TestRuleInputCannotInjectCommands(t *testing.T) {
	c, s := fixture(t)
	s.Rules[0].TargetIP = "198.51.100.20; flush ruleset"
	if _, err := Render(c.TableName, s, false); err == nil {
		t.Fatal("IP injection accepted")
	}
	c, s = fixture(t)
	if _, err := Render("filter", s, false); err == nil {
		t.Fatal("unowned table accepted")
	}
	s.Rules[0].ListenPort = 22282
	if _, err := Render(c.TableName, s, false); err == nil {
		t.Fatal("SSH redirection accepted")
	}
}
func TestExplicitHTTPProxyAndHeaderCredential(t *testing.T) {
	c, _ := fixture(t)
	var auth, requestURL string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		requestURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"node_id":"0011223344556677","revision":1,"rules":[]}`))
	}))
	defer proxy.Close()
	c.PanelURL = "http://127.0.0.1:9"
	c.AllowLocalHTTP = true
	c.ProxyURL = proxy.URL
	client, err := Client(c)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Poll(context.Background(), client, c, model.Heartbeat{NodeID: c.NodeID})
	if err != nil || s.Revision != 1 {
		t.Fatalf("explicit proxy failed: %v", err)
	}
	if auth != "Bearer "+c.Token || strings.Contains(requestURL, c.Token) {
		t.Fatal("node credential absent or exposed in URL")
	}
	c.PanelURL = "http://198.51.100.2:8090"
	if c.Validate() == nil {
		t.Fatal("plaintext public controller accepted")
	}
}
