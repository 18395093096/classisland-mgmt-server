package grpcserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/18395093096/classisland-mgmt-server/internal/config"
	"github.com/18395093096/classisland-mgmt-server/internal/crypto"
	"github.com/18395093096/classisland-mgmt-server/internal/store"

	pbclient "github.com/18395093096/classisland-mgmt-server/gen/go/Protobuf/Client"
	pbenum "github.com/18395093096/classisland-mgmt-server/gen/go/Protobuf/Enum"
	pbserver "github.com/18395093096/classisland-mgmt-server/gen/go/Protobuf/Server"
	pbservice "github.com/18395093096/classisland-mgmt-server/gen/go/Protobuf/Service"
)

// Server Cyrene_MSP gRPC 服务端
type Server struct {
	store *store.Store
	cfg   *config.Config
	hub   *Hub
	keys  *crypto.KeyPair
}

// NewServer 构造 gRPC 服务端（同时载入/生成 PGP 密钥）
func NewServer(st *store.Store, cfg *config.Config, hub *Hub) *Server {
	keys, err := crypto.LoadOrCreate(cfg.KeyDir)
	if err != nil {
		log.Fatalf("载入服务器 PGP 密钥失败：%v", err)
	}
	log.Printf("服务器密钥已就绪：KeyID=%X", keys.KeyID)
	return &Server{store: st, cfg: cfg, hub: hub, keys: keys}
}

// Serve 在指定地址启动 gRPC 服务
func (s *Server) Serve(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	g := grpc.NewServer()
	pbservice.RegisterClientRegisterServer(g, s)
	pbservice.RegisterHandshakeServer(g, s)
	pbservice.RegisterClientCommandDeliverServer(g, s)
	pbservice.RegisterConfigUploadServer(g, s)
	pbservice.RegisterAuditServer(g, s)
	log.Printf("gRPC 服务已启动：%s", addr)
	return g.Serve(lis)
}

// ---------- ClientRegister ----------

func (s *Server) Register(ctx context.Context, req *pbclient.ClientRegisterCsReq) (*pbserver.ClientRegisterScRsp, error) {
	log.Printf("注册请求：uid=%s id=%s mac=%s", req.ClientUid, req.ClientId, req.ClientMac)
	isNew, err := s.store.UpsertClient(req.ClientUid, req.ClientId, req.ClientMac)
	if err != nil {
		return &pbserver.ClientRegisterScRsp{
			Retcode: pbenum.Retcode_ServerInternalError,
			Message: err.Error(),
		}, nil
	}
	code := pbenum.Retcode_Success
	if isNew {
		code = pbenum.Retcode_Registered
	}
	return &pbserver.ClientRegisterScRsp{
		Retcode:         code,
		Message:         "注册成功",
		ServerPublicKey: s.keys.PublicKey,
	}, nil
}

func (s *Server) UnRegister(ctx context.Context, req *pbclient.ClientRegisterCsReq) (*pbserver.ClientRegisterScRsp, error) {
	log.Printf("注销请求：uid=%s", req.ClientUid)
	return &pbserver.ClientRegisterScRsp{Retcode: pbenum.Retcode_Success, Message: "已注销"}, nil
}

// ---------- Handshake ----------

func (s *Server) BeginHandshake(ctx context.Context, req *pbclient.HandshakeScBeginHandShakeReq) (*pbserver.HandshakeScBeginHandShakeRsp, error) {
	token, err := s.keys.Decrypt(req.ChallengeTokenEncrypted)
	if err != nil {
		log.Printf("解密挑战令牌失败：%v", err)
		return &pbserver.HandshakeScBeginHandShakeRsp{
			Retcode: pbenum.Retcode_ServerInternalError,
			Message: "无法解密挑战令牌：" + err.Error(),
		}, nil
	}
	return &pbserver.HandshakeScBeginHandShakeRsp{
		Retcode:                 pbenum.Retcode_Success,
		Message:                 "ok",
		ChallengeTokenDecrypted: token,
		ServerPublicKey:         s.keys.PublicKey,
	}, nil
}

func (s *Server) CompleteHandshake(ctx context.Context, req *pbclient.HandshakeScCompleteHandshakeReq) (*pbserver.HandshakeScCompleteHandshakeRsp, error) {
	if !req.Accepted {
		return &pbserver.HandshakeScCompleteHandshakeRsp{
			Retcode: pbenum.Retcode_HandshakeClientRejected,
			Message: "客户端拒绝了服务器",
		}, nil
	}
	cuid := metaCUID(ctx)
	sessionID, err := newSessionID()
	if err != nil {
		return &pbserver.HandshakeScCompleteHandshakeRsp{
			Retcode: pbenum.Retcode_ServerInternalError,
			Message: err.Error(),
		}, nil
	}
	if err := s.store.SaveSession(sessionID, cuid); err != nil {
		log.Printf("保存会话失败：%v", err)
	}
	return &pbserver.HandshakeScCompleteHandshakeRsp{
		Retcode:   pbenum.Retcode_Success,
		Message:   "握手完成",
		SessionId: sessionID,
	}, nil
}

// ---------- ClientCommandDeliver ----------

func (s *Server) ListenCommand(stream pbservice.ClientCommandDeliver_ListenCommandServer) error {
	cuid := metaCUID(stream.Context())
	if cuid == "" {
		return status.Error(codes.InvalidArgument, "缺少 cuid 元数据")
	}
	ch := s.hub.Attach(cuid)
	defer s.hub.Detach(cuid)

	// 离线期间排队的命令先补发
	for _, c := range s.hub.DrainPending(cuid) {
		if err := stream.Send(&pbserver.ClientCommandDeliverScRsp{
			RetCode: pbenum.Retcode_Success,
			Type:    pbenum.CommandTypes(c.Type),
			Payload: c.Payload,
		}); err != nil {
			return err
		}
	}

	errCh := make(chan error, 1)
	go func() {
		for {
			in, err := stream.Recv()
			if err != nil {
				errCh <- err
				return
			}
			switch in.Type {
			case pbenum.CommandTypes_Ping:
				s.hub.Pong(cuid)
				// 回一个 Pong，客户端据此判断链路正常
				_ = stream.Send(&pbserver.ClientCommandDeliverScRsp{
					RetCode: pbenum.Retcode_Success,
					Type:    pbenum.CommandTypes_Pong,
				})
			default:
				// 客户端在命令流中不会主动上行业务数据
			}
		}
	}()

	for {
		select {
		case err := <-errCh:
			return err
		case cmd := <-ch:
			if err := stream.Send(&pbserver.ClientCommandDeliverScRsp{
				RetCode: pbenum.Retcode_Success,
				Type:    pbenum.CommandTypes(cmd.Type),
				Payload: cmd.Payload,
			}); err != nil {
				return err
			}
			log.Printf("已向 %s 下发命令 type=%d", cuid, cmd.Type)
		case <-time.After(90 * time.Second):
			// 空闲也保持连接，等待 Recv 报错退出
		}
	}
}

// ---------- ConfigUpload ----------

func (s *Server) UploadConfig(ctx context.Context, req *pbclient.ConfigUploadScReq) (*pbserver.ConfigUploadScRsp, error) {
	cuid := metaCUID(ctx)
	dir := filepath.Join("data", "uploads", cuid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &pbserver.ConfigUploadScRsp{Retcode: pbenum.Retcode_ServerInternalError, Message: err.Error()}, nil
	}
	name := req.RequestGuidId
	if name == "" {
		name = time.Now().Format("20060102-150405")
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(req.Payload), 0o644); err != nil {
		return &pbserver.ConfigUploadScRsp{Retcode: pbenum.Retcode_ServerInternalError, Message: err.Error()}, nil
	}
	log.Printf("收到 %s 上传的配置：%s（%d 字节）", cuid, name, len(req.Payload))
	return &pbserver.ConfigUploadScRsp{Retcode: pbenum.Retcode_Success, Message: "ok"}, nil
}

// ---------- Audit ----------

func (s *Server) LogEvent(ctx context.Context, req *pbclient.AuditScReq) (*pbserver.AuditScRsp, error) {
	cuid := metaCUID(ctx)
	if err := s.store.AddAudit(cuid, int(req.Event), req.Payload, req.TimestampUtc); err != nil {
		return &pbserver.AuditScRsp{Retcode: pbenum.Retcode_ServerInternalError, Message: err.Error()}, nil
	}
	return &pbserver.AuditScRsp{Retcode: pbenum.Retcode_Success, Message: "ok"}, nil
}

// ---------- 辅助 ----------

func metaCUID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	v := md.Get("cuid")
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
