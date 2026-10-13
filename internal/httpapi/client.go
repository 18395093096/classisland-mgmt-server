// Package httpapi 实现客户端拉取接口与管理接口。
package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/18395093096/classisland-mgmt-server/internal/model"
	"github.com/18395093096/classisland-mgmt-server/internal/store"
)

// ClientAPI 客户端数据分发接口（对应 Cyrene_MSP 的 Client API）
type ClientAPI struct {
	Store           *store.Store
	BaseURL         string // 对外基址，如 http://192.168.1.10:50050
	OrganizationName string
}

// kindToField 档案类型 -> 清单字段
var kindToField = map[model.ObjectKind]*model.ReVersionString{}

// Manifest 生成某个客户端的集控清单。
// URL 中的 {cuid} 会被客户端替换为自身 GUID，{id} 替换为班级标识，{host} 替换为服务器地址。
func (a *ClientAPI) Manifest(cuid string) (*model.ManagementManifest, error) {
	m := &model.ManagementManifest{
		ServerKind:       model.ServerKindManagementServer,
		OrganizationName: a.OrganizationName,
		CoreVersion:      model.CoreVersion,
	}

	groupID := int64(1)
	if c, err := a.Store.GetClient(cuid); err == nil {
		groupID = c.GroupID
	}

	// 未配置对外基址时使用 {host} 模板，客户端会把它替换成自己配置的服务器地址，
	// 这样部署到未知域名时也无需改配置。
	base := a.BaseURL
	if base == "" {
		base = "{host}"
	}

	set := func(dst *model.ReVersionString, kind model.ObjectKind) {
		obj, err := a.Store.AssignedObject(groupID, string(kind))
		if err != nil {
			// 该类型没有档案：不分发
			*dst = model.ReVersionString{Version: 0, Value: ""}
			return
		}
		*dst = model.ReVersionString{
			Version: int(obj.Version),
			Value:   base + "/api/v1/client/{cuid}/resource/" + string(kind),
		}
	}

	set(&m.ClassPlanSource, model.KindClassPlan)
	set(&m.TimeLayoutSource, model.KindTimeLayout)
	set(&m.SubjectsSource, model.KindSubject)
	set(&m.PolicySource, model.KindPolicy)
	set(&m.DefaultSettingsSource, model.KindSettings)
	set(&m.ComponentsSource, model.KindComponents)
	set(&m.CredentialSource, model.KindCredentials)
	return m, nil
}

// RegisterRoutes 注册客户端接口路由
func (a *ClientAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{
			"service":        "ClassIsland Management Server",
			"protocol":       "Cyrene_MSP",
			"protocolVersion": model.CoreVersion,
			"organization":   a.OrganizationName,
		})
	})

	// 客户端清单：GET /api/v1/client/{cuid}/manifest
	mux.HandleFunc("/api/v1/client/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/client/")
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}
		cuid := parts[0]

		switch {
		case len(parts) == 2 && parts[1] == "manifest":
			m, err := a.Manifest(cuid)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, m)
		case len(parts) == 3 && parts[1] == "resource":
			a.serveResource(w, r, cuid, model.ObjectKind(parts[2]))
		default:
			http.NotFound(w, r)
		}
	})
}

// serveResource 返回分配给该客户端的档案内容
func (a *ClientAPI) serveResource(w http.ResponseWriter, r *http.Request, cuid string, kind model.ObjectKind) {
	if !kind.Valid() {
		writeErr(w, http.StatusBadRequest, "未知的档案类型")
		return
	}
	groupID := int64(1)
	if c, err := a.Store.GetClient(cuid); err == nil {
		groupID = c.GroupID
	}
	obj, err := a.Store.AssignedObject(groupID, string(kind))
	if err != nil {
		writeErr(w, http.StatusNotFound, "未找到该类型的档案")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Object-Version", itoa(obj.Version))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(obj.Payload))
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"message": msg, "code": code})
}
