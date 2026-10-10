package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"
)

const Version = "0.1.0"

var IDPattern = regexp.MustCompile(`^[a-f0-9]{16}$`)
var BlockedPorts = []int{80, 443, 1080, 8000, 8080, 8443}

type Rule struct {
	ID         string `json:"id"`
	NodeID     string `json:"node_id"`
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	ListenPort int    `json:"listen_port"`
	TargetIP   string `json:"target_ip"`
	TargetPort int    `json:"target_port"`
	Enabled    bool   `json:"enabled"`
}
type Node struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TokenHash string `json:"-"`
	Revision  uint64 `json:"revision"`
}
type StoredNode struct {
	Node
	CredentialHash string `json:"credential_hash"`
}
type Snapshot struct {
	NodeID   string `json:"node_id"`
	Revision uint64 `json:"revision"`
	Rules    []Rule `json:"rules"`
}
type Heartbeat struct {
	NodeID          string `json:"node_id"`
	AppliedRevision uint64 `json:"applied_revision"`
	AttemptRevision uint64 `json:"attempt_revision"`
	Error           string `json:"error,omitempty"`
	Version         string `json:"version"`
}
type Health struct {
	LastSeen        int64  `json:"last_seen"`
	AppliedRevision uint64 `json:"applied_revision"`
	AttemptRevision uint64 `json:"attempt_revision"`
	Error           string `json:"error,omitempty"`
	Version         string `json:"version"`
	OfflineAlerted  bool   `json:"-"`
}
type Audit struct {
	At     int64  `json:"at"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Detail string `json:"detail"`
}

func Random(bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func Hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func ValidName(s string) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= 64 && !strings.ContainsAny(s, "\r\n\x00")
}
func Protocols(s string) []string {
	if s == "both" {
		return []string{"tcp", "udp"}
	}
	return []string{s}
}
func ValidateRule(r Rule, rules []Rule, protected []int) error {
	if !IDPattern.MatchString(r.ID) || !IDPattern.MatchString(r.NodeID) {
		return errors.New("invalid rule or node ID")
	}
	if !ValidName(r.Name) {
		return errors.New("备注不能为空，最多 64 个字符")
	}
	if r.Protocol != "tcp" && r.Protocol != "udp" && r.Protocol != "both" {
		return errors.New("协议必须是 tcp、udp 或 both")
	}
	for _, p := range []int{r.ListenPort, r.TargetPort} {
		if p < 1 || p > 65535 {
			return errors.New("端口必须在 1–65535 之间")
		}
		for _, blocked := range BlockedPorts {
			if p == blocked {
				return fmt.Errorf("端口 %d 在 po0 默认限制列表中", p)
			}
		}
	}
	for _, p := range append([]int{22, 22282}, protected...) {
		if r.ListenPort == p {
			return fmt.Errorf("入口端口 %d 为受保护端口", p)
		}
	}
	ip, err := netip.ParseAddr(r.TargetIP)
	if err != nil || !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsLoopback() {
		return errors.New("目标必须是有效的单播 IPv4；可以使用内网 IP")
	}
	if r.Enabled {
		for _, old := range rules {
			if old.ID == r.ID || old.NodeID != r.NodeID || !old.Enabled || old.ListenPort != r.ListenPort {
				continue
			}
			if old.Protocol == r.Protocol || old.Protocol == "both" || r.Protocol == "both" {
				return fmt.Errorf("入口端口与规则 %s 冲突", old.Name)
			}
		}
	}
	return nil
}
func ValidateSnapshot(s Snapshot, protected []int) error {
	if !IDPattern.MatchString(s.NodeID) || s.Revision == 0 {
		return errors.New("invalid node ID or revision")
	}
	if len(s.Rules) > 200 {
		return errors.New("最多支持 200 条节点规则")
	}
	ids := map[string]bool{}
	for _, r := range s.Rules {
		if r.NodeID != s.NodeID || ids[r.ID] {
			return errors.New("规则节点不匹配或 ID 重复")
		}
		ids[r.ID] = true
		if err := ValidateRule(r, s.Rules, protected); err != nil {
			return err
		}
	}
	return nil
}
