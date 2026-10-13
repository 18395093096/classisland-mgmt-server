// Command server 启动 ClassIsland 集控服务端。
//
// 默认监听：
//   - :50050  Client API / Management API / WebUI（HTTP）
//   - :50051  Cyrene_MSP（gRPC）
//
// 部署到只提供单个 HTTP 端口的平台时：设置环境变量 PORT，并在配置里把 EnableGrpc 设为 false。
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/18395093096/classisland-mgmt-server/internal/config"
	"github.com/18395093096/classisland-mgmt-server/internal/grpcserver"
	"github.com/18395093096/classisland-mgmt-server/internal/httpapi"
	"github.com/18395093096/classisland-mgmt-server/internal/store"
	"github.com/18395093096/classisland-mgmt-server/internal/webui"
)

func main() {
	var (
		configPath    = flag.String("config", "config.json", "配置文件路径")
		httpAddr      = flag.String("http", "", "覆盖 HTTP 监听地址")
		grpcAddr      = flag.String("grpc", "", "覆盖 gRPC 监听地址")
		dbPath        = flag.String("db", "", "覆盖数据库路径")
		org           = flag.String("org", "", "覆盖组织名称")
		publicURL     = flag.String("public", "", "对外基址，如 http://192.168.1.10:50050")
		noGRPC        = flag.Bool("no-grpc", false, "不启动 gRPC（单端口 HTTP 部署场景）")
		resetPassword = flag.Bool("reset-password", false, "重设管理员密码为随机值并退出")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败：%v", err)
	}
	if *httpAddr != "" {
		cfg.HTTPAddr = *httpAddr
	} else if p := os.Getenv("PORT"); p != "" {
		// 托管平台会通过 PORT 指定唯一可监听的端口
		cfg.HTTPAddr = ":" + p
	}
	if *grpcAddr != "" {
		cfg.GRPCAddr = *grpcAddr
	}
	if *dbPath != "" {
		cfg.DBPath = *dbPath
	}
	if *org != "" {
		cfg.OrganizationName = *org
	}
	if *publicURL != "" {
		cfg.PublicBaseURL = *publicURL
	}
	if *noGRPC {
		cfg.EnableGRPC = false
	}

	// 会话签名密钥：多实例部署时各实例必须持有同一把密钥，因此存放在配置文件里
	secretGenerated := false
	if cfg.SessionSecret == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			log.Fatalf("生成会话密钥失败：%v", err)
		}
		cfg.SessionSecret = hex.EncodeToString(b)
		secretGenerated = true
	}

	// 管理员凭据：首次启动生成随机密码并写入配置
	auth := httpapi.NewAuth(cfg)
	needSave := secretGenerated
	if *resetPassword || cfg.AdminPasswordHash == "" {
		pwd, err := randomPassword(12)
		if err != nil {
			log.Fatalf("生成管理员密码失败：%v", err)
		}
		if err := auth.SetPassword(cfg.AdminUsername, pwd); err != nil {
			log.Fatalf("设置管理员密码失败：%v", err)
		}
		needSave = true
		fmt.Println()
		fmt.Println("================ 管理员凭据 ================")
		fmt.Printf("  用户名：%s\n", cfg.AdminUsername)
		fmt.Printf("  密　码：%s\n", pwd)
		fmt.Println("  请妥善保存，密码以散列形式存放在", *configPath)
		fmt.Println("==========================================")
		fmt.Println()
		if *resetPassword {
			if err := cfg.Save(*configPath); err != nil {
				log.Fatalf("保存配置失败：%v", err)
			}
			return
		}
	}
	if needSave {
		if err := cfg.Save(*configPath); err != nil {
			log.Printf("警告：保存配置失败：%v", err)
		}
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("打开数据库失败：%v", err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	webui.RegisterRoutes(mux)
	auth.RegisterRoutes(mux)

	clientAPI := &httpapi.ClientAPI{
		Store:            st,
		BaseURL:          cfg.PublicURL(),
		OrganizationName: cfg.OrganizationName,
	}
	clientAPI.RegisterRoutes(mux)

	// 固定路径分发，供静态托管模式（ManagementServerKind=0）的客户端使用
	staticAPI := &httpapi.StaticAPI{
		Store:            st,
		BaseURL:          cfg.PublicURL(),
		OrganizationName: cfg.OrganizationName,
	}
	staticAPI.RegisterRoutes(mux)

	importAPI := &httpapi.ImportAPI{Store: st, Auth: auth}
	importAPI.RegisterRoutes(mux)
	importAPI.RegisterSnapshotRoutes(mux)

	hub := grpcserver.NewHub(st, cfg)
	mgmt := &httpapi.ManagementAPI{Store: st, Auth: auth, Cmder: hub}
	mgmt.RegisterRoutes(mux)

	if cfg.EnableGRPC {
		grpcSrv := grpcserver.NewServer(st, cfg, hub)
		go func() {
			if err := grpcSrv.Serve(cfg.GRPCAddr); err != nil {
				log.Printf("gRPC 服务停止：%v", err)
			}
		}()
	} else {
		log.Println("已按配置关闭 gRPC：客户端请使用静态托管模式（ManagementServerKind=0）接入")
	}

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux}
	go func() {
		log.Printf("HTTP 服务已启动：%s （对外基址 %s）", cfg.HTTPAddr, clientAPI.BaseURL)
		log.Printf("网页控制台：%s/ui/", cfg.PublicURL())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务异常：%v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("正在关闭服务…")
	_ = srv.Close()
}

const passwordChars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// randomPassword 生成不含易混淆字符的随机密码
func randomPassword(n int) (string, error) {
	out := make([]byte, n)
	for i := range out {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(passwordChars))))
		if err != nil {
			return "", err
		}
		out[i] = passwordChars[v.Int64()]
	}
	return string(out), nil
}
