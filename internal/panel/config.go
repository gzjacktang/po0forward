package panel

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/gzjacktang/po0forward/internal/model"
)

type Config struct {
	Listen           string  `json:"listen"`
	PublicURL        string  `json:"public_url"`
	StateFile        string  `json:"state_file"`
	ControlPath      string  `json:"control_path"`
	AdminSalt        string  `json:"admin_salt"`
	AdminHash        string  `json:"admin_hash"`
	CookieSecure     bool    `json:"cookie_secure"`
	TelegramToken    string  `json:"telegram_token"`
	TelegramAdminIDs []int64 `json:"telegram_admin_ids"`
}

func NewConfig(publicURL, state string) (Config, string, error) {
	password := model.Random(16)
	c := Config{Listen: "127.0.0.1:8090", PublicURL: strings.TrimRight(publicURL, "/"), StateFile: state, ControlPath: "/sync/" + model.Random(12), CookieSecure: strings.HasPrefix(publicURL, "https://"), TelegramAdminIDs: []int64{}}
	if err := c.SetPassword(password); err != nil {
		return c, "", err
	}
	return c, password, c.Validate()
}
func (c *Config) SetPassword(password string) error {
	if len(password) < 16 || len(password) > 512 {
		return errors.New("管理员密码需要至少 16 个字符")
	}
	salt := model.Random(16)
	key, err := pbkdf2.Key(sha256.New, password, []byte(salt), 250000, 32)
	if err != nil {
		return err
	}
	c.AdminSalt = salt
	c.AdminHash = hex.EncodeToString(key)
	return nil
}
func (c Config) PasswordMatches(password string) bool {
	if len(password) > 512 {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, []byte(c.AdminSalt), 250000, 32)
	expected, decodeErr := hex.DecodeString(c.AdminHash)
	return err == nil && decodeErr == nil && subtle.ConstantTimeCompare(key, expected) == 1
}
func (c Config) Validate() error {
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return errors.New("public_url 需要完整的 HTTPS 域名，可包含端口，不包含路径")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return errors.New("公网面板必须使用 HTTPS；HTTP 仅供本机测试")
	}
	if u.Scheme == "https" && !c.CookieSecure {
		return errors.New("HTTPS 面板必须开启 cookie_secure")
	}
	if !regexp.MustCompile(`^/sync/[a-f0-9]{24}$`).MatchString(c.ControlPath) {
		return errors.New("invalid control_path")
	}
	if len(c.AdminSalt) < 32 || len(c.AdminHash) != 64 || c.StateFile == "" || c.Listen == "" {
		return errors.New("面板配置不完整")
	}
	if c.TelegramToken != "" && len(c.TelegramAdminIDs) == 0 {
		return errors.New("配置机器人时必须明确指定 telegram_admin_ids")
	}
	for _, id := range c.TelegramAdminIDs {
		if id <= 0 {
			return errors.New("只支持私聊管理员的正整数用户 ID")
		}
	}
	return nil
}
