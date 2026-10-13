package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/18395093096/classisland-mgmt-server/internal/config"
)

// sessionTTL 登录有效期
const sessionTTL = 12 * time.Hour

// Auth 管理员登录与会话管理
//
// 令牌是「过期时间 + HMAC 签名」的自包含结构，不依赖进程内存：
// 托管平台可能运行多个实例或随时重启，内存会话会在请求之间丢失。
// 签名密钥存放在配置文件里，各实例读到的是同一份。
type Auth struct {
	cfg *config.Config

	mu       sync.RWMutex
	revoked  map[string]time.Time // 已退出登录的令牌（尽力而为的本地黑名单）
	sessions map[string]time.Time // 兼容：记录已签发令牌，便于统计
}

// NewAuth 创建鉴权器
func NewAuth(cfg *config.Config) *Auth {
	return &Auth{cfg: cfg, sessions: map[string]time.Time{}, revoked: map[string]time.Time{}}
}

// Required 是否要求登录（配置了管理员用户名与密码散列时要求）
func (a *Auth) Required() bool { return a.cfg.AuthEnabled() }

// Check 校验请求是否已通过鉴权。
// 未启用登录时直接放行；启用后接受签名会话令牌或配置里的固定 AdminToken。
func (a *Auth) Check(r *http.Request) bool {
	if !a.Required() {
		return true
	}
	// 托管平台的反向代理会自带一个 Authorization 头（用于平台自身的访问控制），
	// 因此不能只认请求头：需要依次尝试所有候选令牌，任意一个有效即通过。
	for _, tok := range candidateTokens(r) {
		if tok == "" {
			continue
		}
		if a.cfg.AdminToken != "" && tok == a.cfg.AdminToken {
			return true
		}
		if a.validToken(tok) {
			return true
		}
	}
	return false
}

// candidateTokens 收集请求中可能携带的令牌：查询参数优先，其次 Authorization 头
func candidateTokens(r *http.Request) []string {
	var out []string
	if v := r.URL.Query().Get("token"); v != "" {
		out = append(out, v)
	}
	if v := r.URL.Query().Get("session"); v != "" {
		out = append(out, v)
	}
	if v := r.Header.Get("Authorization"); len(v) > 7 && v[:7] == "Bearer " {
		out = append(out, v[7:])
	}
	return out
}

// validToken 校验自包含令牌的签名与有效期
func (a *Auth) validToken(tok string) bool {
	exp, sig, ok := splitToken(tok)
	if !ok {
		return false
	}
	if !a.signatureOK(exp, sig) {
		return false
	}
	if time.Now().After(time.Unix(exp, 0)) {
		return false
	}
	a.mu.RLock()
	_, bad := a.revoked[tok]
	a.mu.RUnlock()
	return !bad
}

// secret 会话签名密钥。
// 这里不使用可变的随机密钥，而是从配置里的密码散列派生：
// 托管平台可能同时运行多个实例，各自生成的随机密钥互不相同会导致登录态在实例间失效，
// 而所有实例读到的 config.json 是同一份，派生结果必然一致。
// 副作用是修改管理员密码后旧令牌立即失效 —— 这是期望行为。
func (a *Auth) secret() string {
	sum := sha256.Sum256([]byte(a.cfg.AdminPasswordHash + "|cims-session"))
	return hex.EncodeToString(sum[:])
}

// sign 对过期时间做 HMAC-SHA256 签名
func (a *Auth) sign(payload string) string {
	return macHex(a.secret(), payload)
}

// signatureOK 校验签名。
// 除当前的派生密钥外，也接受配置文件里的 SessionSecret 所签的令牌 ——
// 滚动部署期间新旧实例会短暂共存，兼容可避免这期间登录态失效。
func (a *Auth) signatureOK(exp int64, sig string) bool {
	payload := fmt.Sprint(exp)
	if hmac.Equal([]byte(sig), []byte(a.sign(payload))) {
		return true
	}
	if a.cfg.SessionSecret != "" {
		return hmac.Equal([]byte(sig), []byte(macHex(a.cfg.SessionSecret, payload)))
	}
	return false
}

func macHex(key, payload string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func splitToken(tok string) (exp int64, sig string, ok bool) {
	i := strings.Index(tok, ".")
	if i <= 0 {
		return 0, "", false
	}
	v, err := strconv.ParseInt(tok[:i], 16, 64)
	if err != nil {
		return 0, "", false
	}
	return v, tok[i+1:], true
}

// Login 校验用户名密码，成功返回会话令牌
func (a *Auth) Login(user, pass string) (string, error) {
	if !a.Required() {
		// 未启用登录时返回一个占位令牌，接口仍然开放
		return "", nil
	}
	if user != a.cfg.AdminUsername {
		return "", errBadCredential
	}
	if err := bcrypt.CompareHashAndPassword([]byte(a.cfg.AdminPasswordHash), []byte(pass)); err != nil {
		return "", errBadCredential
	}
	// 配置了固定 AdminToken 时直接返回它：
	// 托管平台的多实例之间不需要共享任何密钥或内存状态，登录态天然一致。
	if a.cfg.AdminToken != "" {
		return a.cfg.AdminToken, nil
	}
	return a.newToken(), nil
}

// Logout 注销会话（登记到本地黑名单；跨实例同步以服务端重启为准）
func (a *Auth) Logout(token string) {
	if token == "" {
		return
	}
	a.mu.Lock()
	delete(a.sessions, token)
	a.revoked[token] = time.Now().Add(sessionTTL)
	a.mu.Unlock()
}

// newToken 签发一个自包含令牌：十六进制过期时间 + HMAC 签名
func (a *Auth) newToken() string {
	exp := time.Now().Add(sessionTTL).Unix()
	tok := fmt.Sprintf("%x.%s", exp, a.sign(fmt.Sprint(exp)))
	a.mu.Lock()
	a.sessions[tok] = time.Now().Add(sessionTTL)
	a.mu.Unlock()
	return tok
}

// RegisterRoutes 注册登录相关路由
func (a *Auth) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/admin/session", func(w http.ResponseWriter, r *http.Request) {
		// 诊断字段只回传元信息（长度 / 是否存在），不回显令牌本身，
		// 用于排查托管平台反向代理是否丢弃了鉴权参数。
		// 登录状态查询不允许被缓存，否则浏览器会拿到"未登录"的旧响应
		w.Header().Set("Cache-Control", "no-store")
		tok := tokenFromRequest(r)
		_, hasAuth := r.Header["Authorization"]
		writeJSON(w, map[string]any{
			"required": a.Required(),
			"username": a.cfg.AdminUsername,
			"ok":       a.Check(r),
			"diag": map[string]any{
				"tokenLen":           len(tok),
				"hasAuthHeader":      hasAuth,
				"queryTokenLen":      len(r.URL.Query().Get("token")),
				"adminTokenConfigured": a.cfg.AdminToken != "",
				"adminTokenLen":        len(a.cfg.AdminToken),
			},
		})
	})
	mux.HandleFunc("/api/v1/admin/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "方法不允许")
			return
		}
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		tok, err := a.Login(req.Username, req.Password)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
			return
		}
		writeJSON(w, map[string]any{"token": tok, "expiresIn": int(sessionTTL.Seconds())})
	})
	mux.HandleFunc("/api/v1/admin/logout", func(w http.ResponseWriter, r *http.Request) {
		a.Logout(tokenFromRequest(r))
		writeJSON(w, map[string]any{"ok": true})
	})
}

func tokenFromRequest(r *http.Request) string {
	if v := r.Header.Get("Authorization"); len(v) > 7 && v[:7] == "Bearer " {
		return v[7:]
	}
	if v := r.URL.Query().Get("token"); v != "" {
		return v
	}
	if v := r.URL.Query().Get("session"); v != "" {
		return v
	}
	return ""
}

var errBadCredential = &apiError{"用户名或密码错误"}

type apiError struct{ msg string }

func (e *apiError) Error() string { return e.msg }

// hashPassword 生成 bcrypt 散列（供设置管理员密码使用）
func hashPassword(pass string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	return string(b), err
}

// SetPassword 修改管理员密码
func (a *Auth) SetPassword(user, pass string) error {
	h, err := hashPassword(pass)
	if err != nil {
		return err
	}
	a.cfg.AdminUsername = user
	a.cfg.AdminPasswordHash = h
	return nil
}

var _ = json.Marshal
