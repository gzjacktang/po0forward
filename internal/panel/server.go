package panel

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gzjacktang/po0forward/internal/model"
)

//go:embed web/*
var webFiles embed.FS

type session struct {
	CSRF    string
	Expires time.Time
}
type loginWindow struct {
	Attempts int
	Since    time.Time
}
type Server struct {
	Config   Config
	Store    *Store
	mu       sync.Mutex
	sessions map[string]session
	logins   map[string]loginWindow
	Notify   func(string)
}

func NewServer(c Config, store *Store) *Server {
	return &Server{Config: c, Store: store, sessions: map[string]session{}, logins: map[string]loginWindow{}, Notify: func(string) {}}
}
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func failure(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("请求 JSON 格式错误")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("只允许一个 JSON 请求")
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	root, _ := fs.Sub(webFiles, "web")
	static := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.URL.Path == s.Config.ControlPath {
			s.control(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			s.api(w, r)
			return
		}
		if r.Method != http.MethodGet || !(r.URL.Path == "/" || r.URL.Path == "/app.js" || r.URL.Path == "/style.css") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	})
}
func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || len(token) > 128 {
		failure(w, 401, "unauthorized")
		return
	}
	var h model.Heartbeat
	if decode(w, r, &h) != nil {
		failure(w, 400, "invalid request")
		return
	}
	snapshot, events, err := s.Store.Heartbeat(token, h)
	if err != nil {
		failure(w, 401, "unauthorized")
		return
	}
	for _, event := range events {
		s.Notify(event)
	}
	jsonResponse(w, 200, snapshot)
}
func (s *Server) current(r *http.Request) (string, session, bool) {
	cookie, err := r.Cookie("session")
	if err != nil {
		return "", session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[cookie.Value]
	if !ok || time.Now().After(v.Expires) {
		delete(s.sessions, cookie.Value)
		return "", session{}, false
	}
	return cookie.Value, v, true
}
func (s *Server) validOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || origin == s.Config.PublicURL
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.validOrigin(r) {
		failure(w, 403, "origin denied")
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	s.mu.Lock()
	if _, known := s.logins[ip]; !known && len(s.logins) >= 2048 {
		for k, v := range s.logins {
			if time.Since(v.Since) > 5*time.Minute {
				delete(s.logins, k)
			}
		}
		if len(s.logins) >= 2048 {
			s.mu.Unlock()
			failure(w, 429, "登录尝试过多，稍后重试")
			return
		}
	}
	window := s.logins[ip]
	if time.Since(window.Since) > 5*time.Minute {
		window = loginWindow{Since: time.Now()}
	}
	window.Attempts++
	s.logins[ip] = window
	s.mu.Unlock()
	if window.Attempts > 12 {
		failure(w, 429, "登录尝试过多，5 分钟后重试")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decode(w, r, &body); err != nil {
		failure(w, 400, err.Error())
		return
	}
	if !s.Config.PasswordMatches(body.Password) {
		failure(w, 401, "密码不正确")
		return
	}
	id := model.Random(32)
	v := session{CSRF: model.Random(24), Expires: time.Now().Add(12 * time.Hour)}
	s.mu.Lock()
	for k, old := range s.sessions {
		if time.Now().After(old.Expires) {
			delete(s.sessions, k)
		}
	}
	if len(s.sessions) >= 100 {
		s.mu.Unlock()
		failure(w, 429, "会话数量已达到上限")
		return
	}
	s.sessions[id] = v
	delete(s.logins, ip)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "session", Value: id, Path: "/", HttpOnly: true, Secure: s.Config.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	jsonResponse(w, 200, map[string]string{"csrf": v.CSRF})
}
func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/login" && r.Method == http.MethodPost {
		s.login(w, r)
		return
	}
	id, session, ok := s.current(r)
	if !ok {
		failure(w, 401, "请先登录")
		return
	}
	if r.Method != http.MethodGet && (!s.validOrigin(r) || r.Header.Get("X-CSRF-Token") != session.CSRF) {
		failure(w, 403, "请求校验失败，请刷新页面")
		return
	}
	switch {
	case r.URL.Path == "/api/session" && r.Method == http.MethodGet:
		jsonResponse(w, 200, map[string]string{"csrf": session.CSRF})
	case r.URL.Path == "/api/state" && r.Method == http.MethodGet:
		jsonResponse(w, 200, s.Store.View())
	case r.URL.Path == "/api/logout" && r.Method == http.MethodPost:
		s.mu.Lock()
		delete(s.sessions, id)
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.Config.CookieSecure, SameSite: http.SameSiteStrictMode})
		jsonResponse(w, 200, map[string]bool{"ok": true})
	case r.URL.Path == "/api/nodes" && r.Method == http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		if err := decode(w, r, &body); err != nil {
			failure(w, 400, err.Error())
			return
		}
		node, token, err := s.Store.AddNode(strings.TrimSpace(body.Name), "web")
		if err != nil {
			failure(w, 400, err.Error())
			return
		}
		jsonResponse(w, 201, map[string]any{"node": node, "agent_config": map[string]any{"panel_url": s.Config.PublicURL, "control_path": s.Config.ControlPath, "node_id": node.ID, "token": token, "proxy_url": "", "table_name": "pc_" + node.ID, "state_file": "/var/lib/po0forward/agent-state.json", "protected_ports": []int{22, 22282}, "dry_run": true, "allow_local_http": strings.HasPrefix(s.Config.PublicURL, "http://")}})
	case r.URL.Path == "/api/rules" && r.Method == http.MethodPost:
		var rule model.Rule
		if err := decode(w, r, &rule); err != nil {
			failure(w, 400, err.Error())
			return
		}
		rule.Name = strings.TrimSpace(rule.Name)
		rule, err := s.Store.AddRule(rule, "web", nil)
		if err != nil {
			failure(w, 400, err.Error())
			return
		}
		s.Notify("已保存规则，等待节点应用: " + rule.Name)
		jsonResponse(w, 201, rule)
	case strings.HasPrefix(r.URL.Path, "/api/rules/") && r.Method == http.MethodPost:
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/rules/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		if err := s.Store.ChangeRule(parts[0], parts[1], "web", nil); err != nil {
			failure(w, 400, err.Error())
			return
		}
		s.Notify("已保存规则操作: " + parts[1] + " " + parts[0])
		jsonResponse(w, 200, map[string]bool{"ok": true})
	default:
		http.NotFound(w, r)
	}
}
func (s *Server) Run(ctx context.Context) error {
	server := &http.Server{Addr: s.Config.Listen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				for _, event := range s.Store.OfflineEvents(now) {
					s.Notify(event)
				}
			}
		}
	}()
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
