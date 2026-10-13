// Package config 处理服务端运行配置。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config 服务端配置
type Config struct {
	// HTTPAddr Client API / Management API 监听地址
	HTTPAddr string `json:"HttpAddr"`
	// GRPCAddr gRPC（Cyrene_MSP）监听地址
	GRPCAddr string `json:"GrpcAddr"`
	// DBPath SQLite 数据库路径
	DBPath string `json:"DbPath"`
	// KeyDir PGP 密钥存放目录
	KeyDir string `json:"KeyDir"`
	// OrganizationName 组织名称，会显示在客户端「由 XX 管理」处
	OrganizationName string `json:"OrganizationName"`
	// PublicBaseURL 对外可访问的 HTTP 基址，用于拼接清单里的资源 URL。
	// 留空时使用 http://{监听地址} 自动推导。
	PublicBaseURL string `json:"PublicBaseUrl"`
	// AdminToken 管理 API 的固定访问令牌（可选；设置了它就无需登录即可调用接口）
	AdminToken string `json:"AdminToken"`
	// AdminUsername 管理员用户名；为空表示不启用登录（内网直连场景）
	AdminUsername string `json:"AdminUsername"`
	// AdminPasswordHash 管理员密码的 bcrypt 散列，首次启动会自动生成随机密码并写入
	AdminPasswordHash string `json:"AdminPasswordHash"`
	// EnableGRPC 是否启动 gRPC（Cyrene_MSP）。托管到只提供单 HTTP 端口的平台时应关闭。
	EnableGRPC bool `json:"EnableGrpc"`
	// SessionSecret 会话令牌的签名密钥（首次启动自动生成）。
	// 用签名令牌而非内存会话，是为了让多实例/重启后登录态依然有效。
	SessionSecret string `json:"SessionSecret"`
}

// AuthEnabled 是否启用了管理员登录
func (c *Config) AuthEnabled() bool {
	return c.AdminUsername != "" && c.AdminPasswordHash != ""
}

// Default 返回默认配置
func Default() *Config {
	return &Config{
		HTTPAddr:         ":50050",
		GRPCAddr:         ":50051",
		DBPath:           "data/cims.db",
		KeyDir:           "data/keys",
		OrganizationName: "鹏城信息",
		PublicBaseURL:    "",
		AdminToken:       "",
		AdminUsername:    "admin",
		AdminPasswordHash: "",
		EnableGRPC:       true,
	}
}

// Load 从文件加载配置，文件不存在时使用默认值并写回。
func Load(path string) (*Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Save 保存配置到文件
func (c *Config) Save(path string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// PublicURL 返回对外基址；未配置时返回空串，
// 由调用方按场景处理（客户端模式用 {host} 模板，静态模式按请求域名推导）。
func (c *Config) PublicURL() string {
	if c.PublicBaseURL != "" {
		return trimSlash(c.PublicBaseURL)
	}
	return ""
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
