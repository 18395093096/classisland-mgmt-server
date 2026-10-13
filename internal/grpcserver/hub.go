// Package grpcserver 实现 Cyrene_MSP 的 gRPC 服务端（注册、握手、命令流、配置上传、审计）。
package grpcserver

import (
	"log"
	"sync"
	"time"

	"github.com/18395093096/classisland-mgmt-server/internal/config"
	"github.com/18395093096/classisland-mgmt-server/internal/store"
)

// Command 待下发给客户端的指令
type Command struct {
	Type    int
	Payload []byte
}

// Hub 维护在线客户端的命令通道，并负责投递离线期间排队的命令。
type Hub struct {
	store *store.Store
	cfg   *config.Config

	mu    sync.Mutex
	conns map[string]chan Command // cuid -> 命令通道
	seen  map[string]time.Time    // cuid -> 最近心跳
}

// NewHub 创建 Hub
func NewHub(st *store.Store, cfg *config.Config) *Hub {
	return &Hub{store: st, cfg: cfg, conns: map[string]chan Command{}, seen: map[string]time.Time{}}
}

// Attach 客户端建立命令流时调用，返回该客户端的命令通道。
func (h *Hub) Attach(cuid string) chan Command {
	ch := make(chan Command, 32)
	h.mu.Lock()
	h.conns[cuid] = ch
	h.seen[cuid] = time.Now()
	h.mu.Unlock()
	log.Printf("客户端已上线：%s", cuid)
	_ = h.store.TouchClient(cuid)
	return ch
}

// Detach 客户端断开时调用
func (h *Hub) Detach(cuid string) {
	h.mu.Lock()
	if ch, ok := h.conns[cuid]; ok {
		close(ch)
		delete(h.conns, cuid)
	}
	delete(h.seen, cuid)
	h.mu.Unlock()
	log.Printf("客户端已离线：%s", cuid)
}

// Pong 记录心跳
func (h *Hub) Pong(cuid string) {
	h.mu.Lock()
	h.seen[cuid] = time.Now()
	h.mu.Unlock()
	_ = h.store.TouchClient(cuid)
}

// Send 向在线客户端实时推送命令；离线返回 false（命令仍会留在数据库队列中）。
func (h *Hub) Send(cuid string, cmdType int, payload []byte) bool {
	h.mu.Lock()
	ch, ok := h.conns[cuid]
	h.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- Command{Type: cmdType, Payload: payload}:
		return true
	default:
		// 通道满，退回队列等待下次投递
		return false
	}
}

// Online 返回在线客户端列表
func (h *Hub) Online() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.conns))
	for k := range h.conns {
		out = append(out, k)
	}
	return out
}

// DrainPending 取出该客户端待投递的排队命令
func (h *Hub) DrainPending(cuid string) []Command {
	list, err := h.store.PendingCommands(cuid)
	if err != nil || len(list) == 0 {
		return nil
	}
	var out []Command
	for _, it := range list {
		out = append(out, Command{Type: it.Type, Payload: it.Payload})
		_ = h.store.MarkCommandDelivered(it.ID)
	}
	return out
}
