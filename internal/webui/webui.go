// Package webui 提供内嵌的网页管理控制台。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed index.html planner.js cloud.js
var content embed.FS

// Handler 返回控制台的静态文件处理器
func Handler() http.Handler {
	sub, err := fs.Sub(content, ".")
	if err != nil {
		panic(err)
	}
	// 控制台页面与脚本一律不缓存：
	// 部署新版本后浏览器若继续用旧页面，会出现"登录框关不掉""数据加载不出来"等假故障。
	return noStore(http.FileServer(http.FS(sub)))
}

// RegisterRoutes 注册 /ui/ 路由
func RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("/ui/", http.StripPrefix("/ui/", Handler()))
	mux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
}

// noStore 为响应加上禁用缓存的头
func noStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		h.ServeHTTP(w, r)
	})
}
