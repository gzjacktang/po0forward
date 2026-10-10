package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gzjacktang/po0forward/internal/model"
)

func testServer(t *testing.T) (*Server, string) {
	t.Helper()
	c, password, err := NewConfig("http://127.0.0.1:8090", filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(c.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(c, store), password
}
func request(s *Server, method, path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func login(t *testing.T, s *Server, password string) (*http.Cookie, string) {
	t.Helper()
	w := request(s, "POST", "/api/login", map[string]string{"password": password}, nil, "")
	if w.Code != 200 {
		t.Fatalf("login: %s", w.Body.String())
	}
	var data map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &data)
	return w.Result().Cookies()[0], data["csrf"]
}
func TestAuthenticatedRuleLifecycle(t *testing.T) {
	s, password := testServer(t)
	if w := request(s, "GET", "/api/state", nil, nil, ""); w.Code != 401 {
		t.Fatal("state exposed without authentication")
	}
	cookie, csrf := login(t, s, password)
	if w := request(s, "POST", "/api/nodes", map[string]string{"name": "entry"}, cookie, ""); w.Code != 403 {
		t.Fatal("mutation accepted without CSRF")
	}
	w := request(s, "POST", "/api/nodes", map[string]string{"name": "entry"}, cookie, csrf)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var enrollment struct {
		Node        model.Node     `json:"node"`
		AgentConfig map[string]any `json:"agent_config"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &enrollment)
	rule := model.Rule{NodeID: enrollment.Node.ID, Name: "route", Protocol: "both", ListenPort: 30001, TargetIP: "198.51.100.20", TargetPort: 50123, Enabled: true}
	w = request(s, "POST", "/api/rules", rule, cookie, csrf)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &rule)
	control := func(token, node string, revision uint64) *httptest.ResponseRecorder {
		b, _ := json.Marshal(model.Heartbeat{NodeID: node, AppliedRevision: revision, Version: "test"})
		r := httptest.NewRequest("POST", s.Config.ControlPath, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	token := enrollment.AgentConfig["token"].(string)
	if control("invalid", enrollment.Node.ID, 0).Code != 401 {
		t.Fatal("invalid node credential accepted")
	}
	w = control(token, enrollment.Node.ID, 0)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var snap model.Snapshot
	_ = json.Unmarshal(w.Body.Bytes(), &snap)
	if snap.Revision != 2 || len(snap.Rules) != 1 {
		t.Fatal("desired rules not delivered")
	}
	view := s.Store.View()
	if !view.Nodes[0].Online || view.Nodes[0].AppliedRevision != 0 {
		t.Fatal("unapplied rule reported as applied")
	}
	if control(token, enrollment.Node.ID, 2).Code != 200 || s.Store.View().Nodes[0].AppliedRevision != 2 {
		t.Fatal("acknowledgement lost")
	}
	for _, action := range []string{"disable", "enable", "delete"} {
		w = request(s, "POST", "/api/rules/"+rule.ID+"/"+action, map[string]any{}, cookie, csrf)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	if len(s.Store.View().Rules) != 0 || s.Store.View().Nodes[0].Revision != 5 {
		t.Fatal("lifecycle revision not preserved")
	}
	persisted, err := OpenStore(s.Store.path)
	if err != nil || len(persisted.View().Rules) != 0 || persisted.View().Nodes[0].Revision != 5 {
		t.Fatal("state not durable")
	}
	b, _ := json.Marshal(s.Store.View())
	if strings.Contains(string(b), token) || strings.Contains(string(b), model.Hash(token)) {
		t.Fatal("credential leaked through admin state")
	}
	if w = request(s, "POST", "/api/logout", map[string]any{}, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if request(s, "GET", "/api/state", nil, cookie, "").Code != 401 {
		t.Fatal("logged-out session still accepted")
	}
}
func TestPortConflictAndPersistenceFailureAreNonMutating(t *testing.T) {
	s, _ := testServer(t)
	n, _, _ := s.Store.AddNode("entry", "test")
	r := model.Rule{NodeID: n.ID, Name: "first", Protocol: "tcp", ListenPort: 30001, TargetIP: "198.51.100.10", TargetPort: 50123, Enabled: true}
	first, err := s.Store.AddRule(r, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Name = "second"
	if _, err = s.Store.AddRule(r, "test", nil); err == nil {
		t.Fatal("duplicate active port accepted")
	}
	r.Enabled = false
	second, err := s.Store.AddRule(r, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Store.ChangeRule(second.ID, "enable", "test", nil) == nil {
		t.Fatal("conflicting enable accepted")
	}
	if len(s.Store.View().Rules) != 2 || s.Store.View().Nodes[0].Revision != 3 {
		t.Fatal("failed mutation advanced state")
	}
	bad := filepath.Join(t.TempDir(), "not-a-directory")
	if err = os.WriteFile(bad, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Store.path = filepath.Join(bad, "state.json")
	if err = s.Store.ChangeRule(first.ID, "delete", "test", nil); err == nil {
		t.Fatal("filesystem failure not reported")
	}
	if len(s.Store.View().Rules) != 2 || s.Store.View().Nodes[0].Revision != 3 {
		t.Fatal("memory diverged from durable state")
	}
}
func TestNodeIsolationAndOfflineDebounce(t *testing.T) {
	s, _ := testServer(t)
	a, ta, _ := s.Store.AddNode("a", "test")
	b, tb, _ := s.Store.AddNode("b", "test")
	if _, _, err := s.Store.Heartbeat(ta, model.Heartbeat{NodeID: b.ID}); err == nil {
		t.Fatal("credential crossed node boundary")
	}
	if _, _, err := s.Store.Heartbeat(tb, model.Heartbeat{NodeID: b.ID, AppliedRevision: 999}); err == nil {
		t.Fatal("future ack accepted")
	}
	_, events, err := s.Store.Heartbeat(ta, model.Heartbeat{NodeID: a.ID, AppliedRevision: 1})
	if err != nil || len(events) != 1 {
		t.Fatal("valid ack not accepted")
	}
	if events = s.Store.OfflineEvents(time.Now().Add(46 * time.Second)); len(events) != 1 {
		t.Fatal("offline event missing")
	}
	if events = s.Store.OfflineEvents(time.Now().Add(60 * time.Second)); len(events) != 0 {
		t.Fatal("offline notification repeated")
	}
	_, events, _ = s.Store.Heartbeat(ta, model.Heartbeat{NodeID: a.ID, AppliedRevision: 1})
	if len(events) != 1 || !strings.Contains(events[0], "恢复") {
		t.Fatal("recovery notification missing")
	}
}
func TestTelegramAuthorizationConfirmationAndReplay(t *testing.T) {
	s, _ := testServer(t)
	s.Config.TelegramToken = "dummy-test-token"
	s.Config.TelegramAdminIDs = []int64{101, 202}
	n, _, _ := s.Store.AddNode("entry", "test")
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true,"result":{}}`) }))
	defer mock.Close()
	bot := NewBot(s)
	bot.API = mock.URL
	ctx := context.Background()
	rule := model.Rule{NodeID: n.ID, Name: "chat route", Protocol: "tcp", ListenPort: 30001, TargetIP: "198.51.100.10", TargetPort: 50123, Enabled: true}
	key := bot.savePending(pendingAction{Actor: 101, Kind: "create", Rule: rule, Revision: 1})
	callback := func(actor int64, chatType string) {
		bot.Handle(ctx, telegramUpdate{Callback: &telegramCallback{ID: "test", Data: "confirm:" + key, From: telegramUser{ID: actor}, Message: telegramMessage{Chat: telegramChat{ID: actor, Type: chatType}}}})
	}
	callback(999, "private")
	callback(101, "group")
	callback(202, "private")
	if len(s.Store.View().Rules) != 0 {
		t.Fatal("unauthorized confirmation performed a mutation")
	}
	callback(101, "private")
	callback(101, "private")
	if len(s.Store.View().Rules) != 1 || s.Store.View().Nodes[0].Revision != 2 {
		t.Fatal("valid confirmation failed or replayed")
	}
	stale := bot.savePending(pendingAction{Actor: 101, Kind: "create", Rule: rule, Revision: 1})
	bot.confirm(ctx, 101, stale, false)
	if len(s.Store.View().Rules) != 1 {
		t.Fatal("stale confirmation accepted")
	}
	cancelled := bot.savePending(pendingAction{Actor: 101, Kind: "delete", Rule: s.Store.View().Rules[0], Revision: 2})
	bot.confirm(ctx, 101, cancelled, true)
	if len(s.Store.View().Rules) != 1 {
		t.Fatal("cancel deleted a rule")
	}
	expired := bot.savePending(pendingAction{Actor: 101, Kind: "delete", Rule: s.Store.View().Rules[0], Revision: 2})
	bot.mu.Lock()
	p := bot.pending[expired]
	p.Expires = time.Now().Add(-time.Second)
	bot.pending[expired] = p
	bot.mu.Unlock()
	bot.confirm(ctx, 101, expired, false)
	if len(s.Store.View().Rules) != 1 {
		t.Fatal("expired confirmation deleted a rule")
	}
	for _, action := range []string{"disable", "enable", "delete"} {
		v := s.Store.View()
		key := bot.savePending(pendingAction{Actor: 101, Kind: action, Rule: v.Rules[0], Revision: v.Nodes[0].Revision})
		bot.confirm(ctx, 101, key, false)
	}
	if len(s.Store.View().Rules) != 0 || s.Store.View().Nodes[0].Revision != 5 {
		t.Fatal("confirmed enable/disable/delete lifecycle failed")
	}
}
