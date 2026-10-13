//go:build smoke

// Command smokeclient 模拟 ClassIsland 客户端，端到端验证 Cyrene_MSP：
// 注册 -> PGP 挑战令牌握手 -> 建立命令流 -> 心跳 -> 接收服务端下发的命令。
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"golang.org/x/crypto/openpgp"
	"golang.org/x/crypto/openpgp/armor"

	pbclient "github.com/18395093096/classisland-mgmt-server/gen/go/Protobuf/Client"
	pbenum "github.com/18395093096/classisland-mgmt-server/gen/go/Protobuf/Enum"
	pbservice "github.com/18395093096/classisland-mgmt-server/gen/go/Protobuf/Service"
)

func main() {
	addr := "127.0.0.1:50051"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	cuid := "11111111-2222-3333-4444-555555555555"

	// 支持 https:// 形式的地址（例如经过内网穿透的公网域名）
	creds := insecure.NewCredentials()
	if strings.HasPrefix(addr, "https://") {
		host := strings.TrimPrefix(addr, "https://")
		host = strings.TrimSuffix(host, "/")
		addr = host + ":443"
		creds = credentials.NewTLS(&tls.Config{})
	}
	fmt.Printf("连接 gRPC：%s（%s）\n", addr, map[bool]string{true: "TLS", false: "明文"}[creds != nil])
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 1. 注册
	regCli := pbservice.NewClientRegisterClient(conn)
	rsp, err := regCli.Register(ctx, &pbclient.ClientRegisterCsReq{
		ClientUid: cuid,
		ClientId:  "高一1班",
		ClientMac: "AABBCCDDEEFF",
	})
	if err != nil {
		log.Fatalf("注册失败：%v", err)
	}
	fmt.Printf("[1] 注册返回 retcode=%v message=%q 公钥长度=%d\n", rsp.Retcode, rsp.Message, len(rsp.ServerPublicKey))
	if rsp.ServerPublicKey == "" {
		log.Fatal("服务器未返回公钥")
	}

	// 2. 用服务器公钥加密挑战令牌
	token := randomToken()
	encrypted, err := encryptWithPublicKey(rsp.ServerPublicKey, token)
	if err != nil {
		log.Fatalf("加密挑战令牌失败：%v", err)
	}

	hs := pbservice.NewHandshakeClient(conn)
	begin, err := hs.BeginHandshake(ctx, &pbclient.HandshakeScBeginHandShakeReq{
		ClientUid:               cuid,
		ClientMac:               "AABBCCDDEEFF",
		ChallengeTokenEncrypted: encrypted,
		RequestedServerKeyId:    0,
	})
	if err != nil {
		log.Fatalf("握手失败：%v", err)
	}
	accepted := begin.ChallengeTokenDecrypted == token
	fmt.Printf("[2] 握手返回 retcode=%v 令牌比对=%v\n", begin.Retcode, accepted)
	if !accepted {
		fmt.Printf("    期望=%q 实际=%q\n", token, begin.ChallengeTokenDecrypted)
		os.Exit(1)
	}

	complete, err := hs.CompleteHandshake(ctx, &pbclient.HandshakeScCompleteHandshakeReq{Accepted: true})
	if err != nil {
		log.Fatalf("完成握手失败：%v", err)
	}
	fmt.Printf("[3] 会话建立 retcode=%v session=%s\n", complete.Retcode, complete.SessionId)

	// 3. 建立命令流
	md := metadata.New(map[string]string{
		"cuid":             cuid,
		"protocol_name":    "Cyrene_MSP",
		"protocol_version": "2.0.0.0",
		"session":          complete.SessionId,
	})
	streamCtx := metadata.NewOutgoingContext(ctx, md)
	deliver := pbservice.NewClientCommandDeliverClient(conn)
	stream, err := deliver.ListenCommand(streamCtx)
	if err != nil {
		log.Fatalf("建立命令流失败：%v", err)
	}

	// 4. 心跳
	if err := stream.Send(&pbclient.ClientCommandDeliverScReq{Type: pbenum.CommandTypes_Ping}); err != nil {
		log.Fatalf("发送心跳失败：%v", err)
	}
	msg, err := stream.Recv()
	if err != nil {
		log.Fatalf("接收心跳响应失败：%v", err)
	}
	fmt.Printf("[4] 心跳响应 type=%v retcode=%v\n", msg.Type, msg.RetCode)

	// 5. 等待服务端下发命令（另开一个终端调用管理接口下发）
	fmt.Println("[5] 等待 20 秒，期间请用管理接口下发命令：")
	fmt.Printf("    curl -X POST http://127.0.0.1:50050/api/v1/admin/clients/%s/command -H 'Content-Type: application/json' -d '{\"type\":103}'\n", cuid)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			m, err := stream.Recv()
			if err != nil {
				return
			}
			if m.Type == pbenum.CommandTypes_Pong {
				continue
			}
			fmt.Printf("    收到命令 type=%v payload=%q\n", m.Type, string(m.Payload))
			close(done)
			return
		}
	}()
	select {
	case <-done:
		fmt.Println("[6] 命令下发验证通过")
	case <-time.After(20 * time.Second):
		fmt.Println("[6] 20 秒内未收到命令（可稍后手动验证）")
	}
}

func randomToken() string {
	b := make([]byte, 64)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func encryptWithPublicKey(pub, plaintext string) (string, error) {
	el, err := openpgp.ReadArmoredKeyRing(strings.NewReader(pub))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	aw, err := armor.Encode(&buf, "PGP MESSAGE", nil)
	if err != nil {
		return "", err
	}
	w, err := openpgp.Encrypt(aw, el, nil, nil, nil)
	if err != nil {
		return "", err
	}
	if _, err := w.Write([]byte(plaintext)); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	if err := aw.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}
