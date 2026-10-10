package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gzjacktang/po0forward/internal/model"
)

type telegramUser struct {
	ID int64 `json:"id"`
}
type telegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type telegramMessage struct {
	Text string       `json:"text"`
	Chat telegramChat `json:"chat"`
	From telegramUser `json:"from"`
}
type telegramCallback struct {
	ID      string          `json:"id"`
	Data    string          `json:"data"`
	From    telegramUser    `json:"from"`
	Message telegramMessage `json:"message"`
}
type telegramUpdate struct {
	ID       int64             `json:"update_id"`
	Message  *telegramMessage  `json:"message,omitempty"`
	Callback *telegramCallback `json:"callback_query,omitempty"`
}
type button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}
type pendingAction struct {
	Actor    int64
	Kind     string
	Rule     model.Rule
	Revision uint64
	Expires  time.Time
}
type ruleDraft struct {
	NodeID  string
	Expires time.Time
}
type Bot struct {
	Server  *Server
	API     string
	Client  *http.Client
	Queue   chan string
	mu      sync.Mutex
	pending map[string]pendingAction
	drafts  map[int64]ruleDraft
}

func NewBot(s *Server) *Bot {
	return &Bot{Server: s, API: "https://api.telegram.org", Client: &http.Client{Timeout: 40 * time.Second}, Queue: make(chan string, 100), pending: map[string]pendingAction{}, drafts: map[int64]ruleDraft{}}
}
func (b *Bot) Notify(message string) {
	if b.Server.Config.TelegramToken == "" {
		return
	}
	select {
	case b.Queue <- message:
	default:
		log.Print("telegram notification queue full")
	}
}
func (b *Bot) allowed(id int64) bool {
	for _, allowed := range b.Server.Config.TelegramAdminIDs {
		if allowed == id {
			return true
		}
	}
	return false
}
func (b *Bot) call(ctx context.Context, method string, body any, result any) error {
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", b.API+"/bot"+b.Server.Config.TelegramToken+"/"+method, bytes.NewReader(payload))
	if err != nil {
		return errors.New("telegram request configuration invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := b.Client.Do(req)
	if err != nil {
		return errors.New("telegram API network error")
	}
	defer response.Body.Close()
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&envelope) != nil || !envelope.OK {
		return fmt.Errorf("telegram API rejected request (HTTP %d)", response.StatusCode)
	}
	if result != nil {
		return json.Unmarshal(envelope.Result, result)
	}
	return nil
}
func (b *Bot) send(ctx context.Context, chat int64, text string, rows [][]button) {
	// Never address a chat outside the configured administrators.
	if !b.allowed(chat) {
		return
	}
	body := map[string]any{"chat_id": chat, "text": text}
	if rows != nil {
		body["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	if err := b.call(ctx, "sendMessage", body, nil); err != nil {
		log.Print(err)
	}
}
func (b *Bot) menu(ctx context.Context, id int64) {
	b.send(ctx, id, "节点控制台\n查询、新增、启停或删除转发规则。所有写操作均需再次确认。", [][]button{{{Text: "节点状态", Data: "nodes"}, {Text: "转发规则", Data: "rules"}}, {{Text: "新增规则", Data: "add"}}})
}
func (b *Bot) nodes(ctx context.Context, id int64) {
	v := b.Server.Store.View()
	lines := []string{"节点状态"}
	for _, n := range v.Nodes {
		status := "离线"
		if n.Online {
			status = "在线"
		}
		lines = append(lines, fmt.Sprintf("%s · %s · 已应用 %d / 目标 %d", n.Name, status, n.AppliedRevision, n.Revision))
		if n.Error != "" {
			errorText := []rune(n.Error)
			if len(errorText) > 60 {
				errorText = append(errorText[:60], []rune("…（网页查看详情）")...)
			}
			lines = append(lines, "提示: "+string(errorText))
		}
	}
	b.send(ctx, id, strings.Join(lines, "\n"), nil)
}
func ruleText(r model.Rule) string {
	status := "停用"
	if r.Enabled {
		status = "启用"
	}
	return fmt.Sprintf("%s [%s]\n%s %d → %s:%d\nID: %s", r.Name, status, strings.ToUpper(r.Protocol), r.ListenPort, r.TargetIP, r.TargetPort, r.ID)
}
func (b *Bot) rules(ctx context.Context, id int64) {
	b.rulePage(ctx, id, 0)
}
func (b *Bot) rulePage(ctx context.Context, id int64, page int) {
	v := b.Server.Store.View()
	if len(v.Rules) == 0 {
		b.send(ctx, id, "还没有转发规则。发送 /add 新增。", nil)
		return
	}
	const pageSize = 6
	pages := (len(v.Rules) + pageSize - 1) / pageSize
	if page < 0 || page >= pages {
		b.send(ctx, id, "页面已失效，请重新发送 /rules。", nil)
		return
	}
	text := []string{fmt.Sprintf("转发规则 · %d / %d 页", page+1, pages)}
	rows := [][]button{}
	end := (page + 1) * pageSize
	if end > len(v.Rules) {
		end = len(v.Rules)
	}
	for i, r := range v.Rules[page*pageSize : end] {
		action := "enable"
		label := "启用"
		if r.Enabled {
			action = "disable"
			label = "停用"
		}
		name := ""
		for _, n := range v.Nodes {
			if n.ID == r.NodeID {
				name = n.Name
			}
		}
		text = append(text, fmt.Sprintf("\n%d. %s\n%s", i+1, name, ruleText(r)))
		rows = append(rows, []button{{Text: fmt.Sprintf("%s %d", label, i+1), Data: action + ":" + r.ID}, {Text: fmt.Sprintf("删除 %d", i+1), Data: "delete:" + r.ID}})
	}
	navigation := []button{}
	if page > 0 {
		navigation = append(navigation, button{Text: "上一页", Data: fmt.Sprintf("rules:%d", page-1)})
	}
	if page+1 < pages {
		navigation = append(navigation, button{Text: "下一页", Data: fmt.Sprintf("rules:%d", page+1)})
	}
	if len(navigation) > 0 {
		rows = append(rows, navigation)
	}
	b.send(ctx, id, strings.Join(text, "\n"), rows)
}
func (b *Bot) chooseNode(ctx context.Context, id int64) {
	v := b.Server.Store.View()
	rows := [][]button{}
	for _, n := range v.Nodes {
		rows = append(rows, []button{{Text: n.Name, Data: "node:" + n.ID}})
	}
	if len(rows) == 0 {
		b.send(ctx, id, "请先在网页添加节点。", nil)
		return
	}
	b.send(ctx, id, "选择要新增转发规则的节点：", rows)
}
func (b *Bot) savePending(p pendingAction) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, old := range b.pending {
		if time.Now().After(old.Expires) {
			delete(b.pending, id)
		}
	}
	key := model.Random(12)
	p.Expires = time.Now().Add(2 * time.Minute)
	b.pending[key] = p
	return key
}
func (b *Bot) confirmation(ctx context.Context, id int64, p pendingAction) {
	key := b.savePending(p)
	kind := map[string]string{"create": "新增", "delete": "删除", "enable": "启用", "disable": "停用"}[p.Kind]
	note := ""
	if p.Kind == "disable" || p.Kind == "delete" {
		note = "\n生效后阻止新的转发连接，已有连接可能继续至结束。"
	}
	b.send(ctx, id, "确认"+kind+"以下规则？\n"+ruleText(p.Rule)+note+"\n确认按钮 2 分钟内有效。", [][]button{{{Text: "确认" + kind, Data: "confirm:" + key}, {Text: "取消", Data: "cancel:" + key}}})
}
func (b *Bot) prepare(ctx context.Context, id int64, kind, ruleID string) {
	v := b.Server.Store.View()
	for _, r := range v.Rules {
		if r.ID == ruleID {
			for _, n := range v.Nodes {
				if n.ID == r.NodeID {
					b.confirmation(ctx, id, pendingAction{Actor: id, Kind: kind, Rule: r, Revision: n.Revision})
					return
				}
			}
		}
	}
	b.send(ctx, id, "找不到这条规则，请重新查询。", nil)
}
func (b *Bot) draft(ctx context.Context, id int64, text string) bool {
	b.mu.Lock()
	draft, ok := b.drafts[id]
	b.mu.Unlock()
	if !ok || time.Now().After(draft.Expires) {
		return false
	}
	fields := strings.Fields(text)
	if len(fields) < 4 {
		b.send(ctx, id, "格式：TCP 入口端口 目标IPv4 目标端口 备注\n例如：TCP 30001 198.51.100.20 50123 香港入口", nil)
		return true
	}
	port, e1 := strconv.Atoi(fields[1])
	targetPort, e2 := strconv.Atoi(fields[3])
	name := strings.Join(fields[4:], " ")
	if name == "" {
		name = "转发 " + fields[1]
	}
	r := model.Rule{ID: model.Random(8), NodeID: draft.NodeID, Name: name, Protocol: strings.ToLower(fields[0]), ListenPort: port, TargetIP: fields[2], TargetPort: targetPort, Enabled: true}
	v := b.Server.Store.View()
	var revision uint64
	for _, n := range v.Nodes {
		if n.ID == r.NodeID {
			revision = n.Revision
		}
	}
	if e1 != nil || e2 != nil || revision == 0 {
		b.send(ctx, id, "端口或节点不正确，请重新输入。", nil)
		return true
	}
	if err := model.ValidateRule(r, v.Rules, nil); err != nil {
		b.send(ctx, id, err.Error(), nil)
		return true
	}
	b.mu.Lock()
	delete(b.drafts, id)
	b.mu.Unlock()
	b.confirmation(ctx, id, pendingAction{Actor: id, Kind: "create", Rule: r, Revision: revision})
	return true
}
func (b *Bot) confirm(ctx context.Context, id int64, key string, cancel bool) {
	b.mu.Lock()
	p, ok := b.pending[key]
	// A different administrator cannot steal or consume another user's action.
	if ok && p.Actor == id {
		delete(b.pending, key)
	}
	b.mu.Unlock()
	if !ok || p.Actor != id || time.Now().After(p.Expires) {
		b.send(ctx, id, "确认已失效，请重新操作。", nil)
		return
	}
	if cancel {
		b.send(ctx, id, "已取消。", nil)
		return
	}
	actor := fmt.Sprintf("telegram:%d", id)
	var err error
	if p.Kind == "create" {
		_, err = b.Server.Store.AddRule(p.Rule, actor, &p.Revision)
	} else {
		err = b.Server.Store.ChangeRule(p.Rule.ID, p.Kind, actor, &p.Revision)
	}
	if err != nil {
		b.send(ctx, id, "保存失败: "+err.Error(), nil)
		return
	}
	b.send(ctx, id, "操作已保存，等待节点应用。可用 /nodes 查看应用状态。", nil)
}
func (b *Bot) Handle(ctx context.Context, u telegramUpdate) {
	if u.Callback != nil {
		c := u.Callback
		if !b.allowed(c.From.ID) || c.Message.Chat.Type != "private" || c.Message.Chat.ID != c.From.ID {
			return
		}
		_ = b.call(ctx, "answerCallbackQuery", map[string]string{"callback_query_id": c.ID}, nil)
		switch c.Data {
		case "menu":
			b.menu(ctx, c.From.ID)
		case "nodes":
			b.nodes(ctx, c.From.ID)
		case "rules":
			b.rules(ctx, c.From.ID)
		case "add":
			b.chooseNode(ctx, c.From.ID)
		default:
			parts := strings.SplitN(c.Data, ":", 2)
			if len(parts) != 2 {
				return
			}
			switch parts[0] {
			case "rules":
				page, err := strconv.Atoi(parts[1])
				if err == nil {
					b.rulePage(ctx, c.From.ID, page)
				}
			case "confirm", "cancel":
				b.confirm(ctx, c.From.ID, parts[1], parts[0] == "cancel")
			case "enable", "disable", "delete":
				b.prepare(ctx, c.From.ID, parts[0], parts[1])
			case "node":
				found := false
				for _, n := range b.Server.Store.View().Nodes {
					found = found || n.ID == parts[1]
				}
				if !found {
					return
				}
				b.mu.Lock()
				b.drafts[c.From.ID] = ruleDraft{NodeID: parts[1], Expires: time.Now().Add(5 * time.Minute)}
				b.mu.Unlock()
				b.send(ctx, c.From.ID, "发送规则参数：\nTCP 入口端口 目标IPv4 目标端口 备注\n协议也可用 UDP 或 BOTH。", nil)
			}
		}
		return
	}
	if u.Message == nil {
		return
	}
	m := u.Message
	if !b.allowed(m.From.ID) || m.Chat.Type != "private" || m.Chat.ID != m.From.ID {
		return
	}
	text := strings.TrimSpace(m.Text)
	if !strings.HasPrefix(text, "/") && b.draft(ctx, m.From.ID, text) {
		return
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	command := strings.SplitN(fields[0], "@", 2)[0]
	switch command {
	case "/start", "/menu", "/help":
		b.menu(ctx, m.From.ID)
	case "/nodes":
		b.nodes(ctx, m.From.ID)
	case "/rules":
		b.rules(ctx, m.From.ID)
	case "/add":
		b.chooseNode(ctx, m.From.ID)
	case "/enable", "/disable", "/delete":
		if len(fields) != 2 {
			b.send(ctx, m.From.ID, "请在命令后填写规则 ID。", nil)
			return
		}
		b.prepare(ctx, m.From.ID, strings.TrimPrefix(command, "/"), fields[1])
	case "/cancel":
		b.mu.Lock()
		delete(b.drafts, m.From.ID)
		b.mu.Unlock()
		b.send(ctx, m.From.ID, "已取消新增输入。", nil)
	default:
		b.send(ctx, m.From.ID, "发送 /menu 打开菜单。", nil)
	}
}
func (b *Bot) Run(ctx context.Context) {
	if b.Server.Config.TelegramToken == "" {
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case text := <-b.Queue:
				for _, id := range b.Server.Config.TelegramAdminIDs {
					b.send(ctx, id, text, nil)
				}
			}
		}
	}()
	offset := b.Server.Store.Offset()
	for ctx.Err() == nil {
		var updates []telegramUpdate
		err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message", "callback_query"}}, &updates)
		if err != nil {
			log.Print(err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			if u.ID < offset {
				continue
			}
			b.Handle(ctx, u)
			offset = u.ID + 1
			if err = b.Server.Store.SetOffset(offset); err != nil {
				log.Print("failed to save Telegram update offset")
				return
			}
		}
	}
}
