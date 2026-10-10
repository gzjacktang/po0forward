package panel

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/gzjacktang/po0forward/internal/model"
	"github.com/gzjacktang/po0forward/internal/storage"
)

type State struct {
	Nodes          map[string]model.StoredNode `json:"nodes"`
	Rules          map[string]model.Rule       `json:"rules"`
	Audit          []model.Audit               `json:"audit"`
	TelegramOffset int64                       `json:"telegram_offset"`
}
type NodeView struct {
	model.Node
	model.Health
	Online bool `json:"online"`
}
type View struct {
	Nodes []NodeView    `json:"nodes"`
	Rules []model.Rule  `json:"rules"`
	Audit []model.Audit `json:"audit"`
}
type Store struct {
	mu     sync.Mutex
	path   string
	state  State
	health map[string]model.Health
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, health: map[string]model.Health{}}
	if err := storage.ReadJSON(path, &s.state); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if s.state.Nodes == nil {
		s.state.Nodes = map[string]model.StoredNode{}
	}
	if s.state.Rules == nil {
		s.state.Rules = map[string]model.Rule{}
	}
	return s, nil
}
func (s *Store) transaction(actor, action string, change func(*State) (string, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.state)
	var next State
	if err := json.Unmarshal(b, &next); err != nil {
		return err
	}
	detail, err := change(&next)
	if err != nil {
		return err
	}
	if action != "" {
		next.Audit = append(next.Audit, model.Audit{At: time.Now().Unix(), Actor: actor, Action: action, Detail: detail})
		if len(next.Audit) > 300 {
			next.Audit = next.Audit[len(next.Audit)-300:]
		}
	}
	if err := storage.WriteJSON(s.path, next); err != nil {
		return fmt.Errorf("无法保存配置: %w", err)
	}
	s.state = next
	return nil
}
func (s *Store) AddNode(name, actor string) (model.Node, string, error) {
	if !model.ValidName(name) {
		return model.Node{}, "", errors.New("节点名称不能为空，最多 64 个字符")
	}
	token := model.Random(32)
	node := model.Node{ID: model.Random(8), Name: name, Revision: 1}
	err := s.transaction(actor, "node.create", func(st *State) (string, error) {
		if len(st.Nodes) >= 20 {
			return "", errors.New("最多支持 20 个节点")
		}
		st.Nodes[node.ID] = model.StoredNode{Node: node, CredentialHash: model.Hash(token)}
		return name, nil
	})
	return node, token, err
}
func rulesOf(st *State, node string) []model.Rule {
	rules := []model.Rule{}
	for _, r := range st.Rules {
		if node == "" || r.NodeID == node {
			rules = append(rules, r)
		}
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].NodeID != rules[j].NodeID {
			return rules[i].NodeID < rules[j].NodeID
		}
		if rules[i].ListenPort != rules[j].ListenPort {
			return rules[i].ListenPort < rules[j].ListenPort
		}
		return rules[i].ID < rules[j].ID
	})
	return rules
}
func (s *Store) AddRule(r model.Rule, actor string, expected *uint64) (model.Rule, error) {
	r.ID = model.Random(8)
	err := s.transaction(actor, "rule.create", func(st *State) (string, error) {
		n, ok := st.Nodes[r.NodeID]
		if !ok {
			return "", errors.New("节点不存在")
		}
		if expected != nil && n.Revision != *expected {
			return "", errors.New("规则已变更，请重新确认")
		}
		if len(rulesOf(st, r.NodeID)) >= 200 {
			return "", errors.New("节点规则已达到上限")
		}
		if err := model.ValidateRule(r, rulesOf(st, r.NodeID), nil); err != nil {
			return "", err
		}
		st.Rules[r.ID] = r
		n.Revision++
		st.Nodes[n.ID] = n
		return n.Name + ": " + r.Name, nil
	})
	return r, err
}
func (s *Store) ChangeRule(id, action, actor string, expected *uint64) error {
	return s.transaction(actor, "rule."+action, func(st *State) (string, error) {
		r, ok := st.Rules[id]
		if !ok {
			return "", errors.New("规则不存在")
		}
		n := st.Nodes[r.NodeID]
		if expected != nil && n.Revision != *expected {
			return "", errors.New("规则已变更，请重新确认")
		}
		switch action {
		case "delete":
			delete(st.Rules, id)
		case "enable", "disable":
			r.Enabled = action == "enable"
			if err := model.ValidateRule(r, rulesOf(st, r.NodeID), nil); err != nil {
				return "", err
			}
			st.Rules[id] = r
		default:
			return "", errors.New("不支持的操作")
		}
		n.Revision++
		st.Nodes[n.ID] = n
		return n.Name + ": " + r.Name, nil
	})
}
func (s *Store) View() View {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := View{Nodes: []NodeView{}, Rules: rulesOf(&s.state, ""), Audit: append([]model.Audit{}, s.state.Audit...)}
	for id, n := range s.state.Nodes {
		h := s.health[id]
		v.Nodes = append(v.Nodes, NodeView{Node: n.Node, Health: h, Online: h.LastSeen > 0 && time.Now().Unix()-h.LastSeen < 45})
	}
	sort.Slice(v.Nodes, func(i, j int) bool { return v.Nodes[i].Name < v.Nodes[j].Name })
	return v
}
func (s *Store) Heartbeat(token string, h model.Heartbeat) (model.Snapshot, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.state.Nodes[h.NodeID]
	if !ok || subtle.ConstantTimeCompare([]byte(n.CredentialHash), []byte(model.Hash(token))) != 1 {
		return model.Snapshot{}, nil, errors.New("unauthorized")
	}
	if h.AppliedRevision > n.Revision || h.AttemptRevision > n.Revision || len(h.Error) > 1000 {
		return model.Snapshot{}, nil, errors.New("invalid heartbeat")
	}
	old := s.health[n.ID]
	events := []string{}
	if old.OfflineAlerted {
		events = append(events, "节点恢复在线: "+n.Name)
	}
	if h.Error != "" && (old.Error != h.Error || old.AttemptRevision != h.AttemptRevision) {
		events = append(events, "规则应用失败: "+n.Name+"\n"+h.Error)
	}
	if h.AppliedRevision == n.Revision && h.Error == "" && old.AppliedRevision != n.Revision {
		events = append(events, fmt.Sprintf("节点规则已应用: %s，版本 %d", n.Name, n.Revision))
	}
	s.health[n.ID] = model.Health{LastSeen: time.Now().Unix(), AppliedRevision: h.AppliedRevision, AttemptRevision: h.AttemptRevision, Error: h.Error, Version: h.Version}
	return model.Snapshot{NodeID: n.ID, Revision: n.Revision, Rules: rulesOf(&s.state, n.ID)}, events, nil
}
func (s *Store) OfflineEvents(now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := []string{}
	for id, h := range s.health {
		if h.LastSeen > 0 && !h.OfflineAlerted && now.Unix()-h.LastSeen >= 45 {
			h.OfflineAlerted = true
			s.health[id] = h
			events = append(events, "节点离线超过 45 秒: "+s.state.Nodes[id].Name)
		}
	}
	return events
}
func (s *Store) Offset() int64 { s.mu.Lock(); defer s.mu.Unlock(); return s.state.TelegramOffset }
func (s *Store) SetOffset(offset int64) error {
	return s.transaction("", "", func(st *State) (string, error) { st.TelegramOffset = offset; return "", nil })
}
