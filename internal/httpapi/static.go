package httpapi

import (
	"net/http"
	"strings"

	"github.com/18395093096/classisland-mgmt-server/internal/model"
	"github.com/18395093096/classisland-mgmt-server/internal/store"
)

// requestBase 推导对外基址：配置了就用配置的；否则按当前请求的协议与域名推导，
// 这样部署到未知域名的平台（如托管服务分配的随机域名）时无需预先配置。
func requestBase(r *http.Request, configured string) string {
	if configured != "" {
		return strings.TrimRight(configured, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = strings.TrimSpace(strings.Split(p, ",")[0])
	}
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		return scheme + "://" + strings.TrimSpace(strings.Split(h, ",")[0])
	}
	return scheme + "://" + r.Host
}

// StaticAPI 固定路径的数据分发端点。
// 用途：客户端以静态托管模式（ManagementServerKind=0）接入时，
// 清单 URL 是一个固定地址，无法带 {cuid}，因此这里提供一组不含模板的静态路径。
// 同时它也方便把服务端部署到只提供单个 HTTP 端口的平台。
type StaticAPI struct {
	Store            *store.Store
	BaseURL          string
	OrganizationName string
}

// staticFiles 静态文件名 -> 档案类型
var staticFiles = map[string]model.ObjectKind{
	"/classplans.json":  model.KindClassPlan,
	"/timelayouts.json": model.KindTimeLayout,
	"/subjects.json":    model.KindSubject,
	"/policy.json":      model.KindPolicy,
	"/settings.json":    model.KindSettings,
	"/components.json":  model.KindComponents,
}

// fileNameByKind 档案类型 -> 静态文件名
var fileNameByKind = map[model.ObjectKind]string{
	model.KindClassPlan:   "classplans.json",
	model.KindTimeLayout:  "timelayouts.json",
	model.KindSubject:     "subjects.json",
	model.KindPolicy:      "policy.json",
	model.KindSettings:    "settings.json",
	model.KindComponents:  "components.json",
	model.KindCredentials: "credentials.json",
}

// RegisterRoutes 注册静态分发路由
func (a *StaticAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		// 静态托管模式：ServerKind 必须为 0
		// 未配置对外基址时按当前访问地址动态推导，这样部署到未知域名也无需改配置
		base := requestBase(r, a.BaseURL)
		m := &model.ManagementManifest{
			ServerKind:       model.ServerKindServerless,
			OrganizationName: a.OrganizationName,
			CoreVersion:      model.CoreVersion,
		}
		set := func(dst *model.ReVersionString, kind model.ObjectKind) {
			obj, err := a.Store.AssignedObject(1, string(kind))
			if err != nil {
				*dst = model.ReVersionString{Version: 0, Value: ""}
				return
			}
			*dst = model.ReVersionString{
				Version: int(obj.Version),
				Value:   base + "/" + fileNameByKind[kind],
			}
		}
		// 课表/时间表/科目的静态文件名分别为 classplans/timelayouts/subjects
		if obj, err := a.Store.AssignedObject(1, string(model.KindClassPlan)); err == nil {
			m.ClassPlanSource = model.ReVersionString{Version: int(obj.Version), Value: base + "/classplans.json"}
		}
		if obj, err := a.Store.AssignedObject(1, string(model.KindTimeLayout)); err == nil {
			m.TimeLayoutSource = model.ReVersionString{Version: int(obj.Version), Value: base + "/timelayouts.json"}
		}
		if obj, err := a.Store.AssignedObject(1, string(model.KindSubject)); err == nil {
			m.SubjectsSource = model.ReVersionString{Version: int(obj.Version), Value: base + "/subjects.json"}
		}
		set(&m.PolicySource, model.KindPolicy)
		set(&m.DefaultSettingsSource, model.KindSettings)
		set(&m.ComponentsSource, model.KindComponents)
		writeJSON(w, m)
	})

	for p, kind := range staticFiles {
		path, k := p, kind
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			obj, err := a.Store.AssignedObject(1, string(k))
			if err != nil {
				// 客户端会把该文件反序列化成对象，返回空对象比 404 更安全
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_, _ = w.Write([]byte("{}"))
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("X-Object-Version", itoa(obj.Version))
			_, _ = w.Write([]byte(obj.Payload))
		})
	}
}
