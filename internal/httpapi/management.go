package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/18395093096/classisland-mgmt-server/internal/model"
	"github.com/18395093096/classisland-mgmt-server/internal/store"
)

// ManagementAPI 管理接口：档案、客户端、命令下发
type ManagementAPI struct {
	Store *store.Store
	Auth  *Auth // 鉴权器，未启用登录时所有请求放行
	Cmder Commander
}

// Commander 向在线客户端下发命令的能力，由 gRPC 层实现。
type Commander interface {
	// Send 立即向在线客户端推送命令；离线返回 false
	Send(cuid string, cmdType int, payload []byte) bool
	// Online 返回当前在线（已建立命令流）的客户端
	Online() []string
}

// RegisterRoutes 注册管理接口
func (a *ManagementAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/admin/", func(w http.ResponseWriter, r *http.Request) {
		if !a.auth(r) {
			writeErr(w, http.StatusUnauthorized, "未提供有效令牌")
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/")
		parts := strings.Split(strings.Trim(rest, "/"), "/")
		switch {
		case parts[0] == "objects":
			a.handleObjects(w, r, parts[1:])
		case parts[0] == "clients":
			a.handleClients(w, r, parts[1:])
		case parts[0] == "groups":
			a.handleGroups(w, r, parts[1:])
		case parts[0] == "audit":
			list, err := a.Store.ListAudit(100)
			if err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			writeJSON(w, list)
		case parts[0] == "online":
			var out []string
			if a.Cmder != nil {
				out = a.Cmder.Online()
			}
			writeJSON(w, map[string]any{"online": out})
		default:
			http.NotFound(w, r)
		}
	})
}

func (a *ManagementAPI) auth(r *http.Request) bool {
	if a.Auth == nil {
		return true
	}
	return a.Auth.Check(r)
}

// ---------- 档案 ----------

func (a *ManagementAPI) handleObjects(w http.ResponseWriter, r *http.Request, parts []string) {
	// /objects?kind=classplan
	if len(parts) == 0 || parts[0] == "" {
		switch r.Method {
		case http.MethodGet:
			kind := r.URL.Query().Get("kind")
			if kind == "" {
				var all []store.Object
				for _, k := range model.Kinds {
					list, err := a.Store.ListObjects(string(k))
					if err != nil {
						writeErr(w, 500, err.Error())
						return
					}
					all = append(all, list...)
				}
				writeJSON(w, all)
				return
			}
			list, err := a.Store.ListObjects(kind)
			if err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			writeJSON(w, list)
		case http.MethodPost:
			var req struct {
				Kind    string `json:"kind"`
				Name    string `json:"name"`
				Payload string `json:"payload"`
			}
			if err := readJSON(r, &req); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			if !model.ObjectKind(req.Kind).Valid() {
				writeErr(w, 400, "未知的档案类型")
				return
			}
			obj, err := a.Store.CreateObject(req.Kind, req.Name, req.Payload)
			if err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			writeJSON(w, obj)
		default:
			writeErr(w, 405, "方法不允许")
		}
		return
	}

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeErr(w, 400, "无效的档案 ID")
		return
	}
	switch r.Method {
	case http.MethodGet:
		obj, err := a.Store.GetObject(id)
		if err != nil {
			writeErr(w, 404, "档案不存在")
			return
		}
		writeJSON(w, obj)
	case http.MethodPut, http.MethodPost:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		var req struct {
			Name    string `json:"name"`
			Payload string `json:"payload"`
		}
		_ = json.Unmarshal(body, &req)
		obj, err := a.Store.UpdateObjectPayload(id, req.Payload)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if req.Name != "" {
			_ = a.Store.RenameObject(id, req.Name)
			obj.Name = req.Name
		}
		writeJSON(w, obj)
	case http.MethodDelete:
		if err := a.Store.DeleteObject(id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		writeErr(w, 405, "方法不允许")
	}
}

// ---------- 客户端 ----------

func (a *ManagementAPI) handleClients(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 0 || parts[0] == "" {
		list, err := a.Store.ListClients()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, list)
		return
	}
	cuid := parts[0]
	// 删除客户端（清理退出的、重复的旧实例）
	if len(parts) == 1 && r.Method == http.MethodDelete {
		if err := a.Store.DeleteClient(cuid); err != nil {
			if err == store.ErrNotFound {
				writeErr(w, http.StatusNotFound, "客户端不存在")
				return
			}
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "deleted": cuid})
		return
	}
	// /clients/{cuid}/command
	if len(parts) >= 2 && parts[1] == "command" && r.Method == http.MethodPost {
		var req struct {
			Type    int    `json:"type"`    // 见 protobuf CommandTypes：101 重启 /102 通知 /103 数据更新 /104 取配置
			Payload string `json:"payload"` // 可选，base64 或原始字符串
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		// 先入队，保证离线的客户端下次上线也能收到
		if err := a.Store.EnqueueCommand(cuid, req.Type, []byte(req.Payload)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		sent := false
		if a.Cmder != nil {
			sent = a.Cmder.Send(cuid, req.Type, []byte(req.Payload))
		}
		writeJSON(w, map[string]any{"queued": true, "delivered": sent})
		return
	}
	// /clients/{cuid}/group
	if len(parts) >= 2 && parts[1] == "group" && r.Method == http.MethodPost {
		var req struct {
			GroupID int64 `json:"groupId"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if err := a.Store.SetClientGroup(cuid, req.GroupID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	c, err := a.Store.GetClient(cuid)
	if err != nil {
		writeErr(w, 404, "客户端不存在")
		return
	}
	writeJSON(w, c)
}

// ---------- 分组 ----------

func (a *ManagementAPI) handleGroups(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 0 || parts[0] == "" {
		switch r.Method {
		case http.MethodGet:
			list, err := a.Store.ListGroups()
			if err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			writeJSON(w, list)
		case http.MethodPost:
			var req struct {
				Name string `json:"name"`
			}
			if err := readJSON(r, &req); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			g, err := a.Store.CreateGroup(req.Name)
			if err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			writeJSON(w, g)
		default:
			writeErr(w, 405, "方法不允许")
		}
		return
	}

	// /groups/{id}/assign
	if len(parts) >= 2 && parts[1] == "assign" && r.Method == http.MethodPost {
		gid, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			writeErr(w, 400, "无效的分组 ID")
			return
		}
		var req struct {
			Kind     string `json:"kind"`
			ObjectID int64  `json:"objectId"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if !model.ObjectKind(req.Kind).Valid() {
			writeErr(w, 400, "未知的档案类型")
			return
		}
		if err := a.Store.Assign(gid, req.ObjectID, req.Kind); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	http.NotFound(w, r)
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}
