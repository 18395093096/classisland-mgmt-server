package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/18395093096/classisland-mgmt-server/internal/model"
	"github.com/18395093096/classisland-mgmt-server/internal/store"
)

// ImportAPI 上传导入接口：ClassIsland 备份压缩包 / 单个档案 JSON
type ImportAPI struct {
	Store *store.Store
	Auth  *Auth
}

// RegisterRoutes 注册导入路由
func (a *ImportAPI) RegisterRoutes(mux *http.ServeMux) {
	// 上传 ClassIsland 备份压缩包
	mux.HandleFunc("/api/v1/admin/import/backup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "方法不允许")
			return
		}
		if !a.auth(r) {
			writeErr(w, http.StatusUnauthorized, "请先登录")
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			writeErr(w, http.StatusBadRequest, "请选择要上传的备份文件（.zip）")
			return
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "读取文件失败")
			return
		}
		result, err := importBackup(a.Store, data)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "imported": result})
	})

	// 上传单个档案 JSON（课表 / 时间表 / 科目 …）
	mux.HandleFunc("/api/v1/admin/import/profile", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "方法不允许")
			return
		}
		if !a.auth(r) {
			writeErr(w, http.StatusUnauthorized, "请先登录")
			return
		}
		kind := r.FormValue("kind")
		if !model.ObjectKind(kind).Valid() {
			writeErr(w, http.StatusBadRequest, "未知的档案类型："+kind)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			writeErr(w, http.StatusBadRequest, "请选择要上传的 JSON 文件")
			return
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "读取文件失败")
			return
		}
		payload, err := normalizeProfile(kind, header.Filename, data)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		name := r.FormValue("name")
		if name == "" {
			name = strings.TrimSuffix(header.Filename, path.Ext(header.Filename))
		}
		act, err := upsertObject(a.Store, kind, name, payload)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "action": act, "kind": kind})
	})
}

// RegisterSnapshotRoutes 注册快照导出/恢复接口。
// 快照用于把服务器上的全部档案打包成一份 JSON，交给云端存储保存，
// 从而在重新部署、换机器时一键恢复。
func (a *ImportAPI) RegisterSnapshotRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/admin/export/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if !a.auth(r) {
			writeErr(w, http.StatusUnauthorized, "请先登录")
			return
		}
		out := struct {
			ExportedAt int64 `json:"exportedAt"`
			Objects    []map[string]any `json:"objects"`
		}{ExportedAt: time.Now().Unix(), Objects: []map[string]any{}}
		for _, k := range model.Kinds {
			list, err := a.Store.ListObjects(string(k))
			if err != nil {
				continue
			}
			for _, o := range list {
				out.Objects = append(out.Objects, map[string]any{
					"kind": o.Kind, "name": o.Name, "payload": o.Payload, "version": o.Version,
				})
			}
		}
		writeJSON(w, out)
	})

	mux.HandleFunc("/api/v1/admin/import/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "方法不允许")
			return
		}
		if !a.auth(r) {
			writeErr(w, http.StatusUnauthorized, "请先登录")
			return
		}
		var req struct {
			Objects []struct {
				Kind    string `json:"kind"`
				Name    string `json:"name"`
				Payload string `json:"payload"`
			} `json:"objects"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		result := map[string]string{}
		for _, o := range req.Objects {
			if !model.ObjectKind(o.Kind).Valid() {
				result[o.Kind] = "跳过：未知类型"
				continue
			}
			act, err := upsertObject(a.Store, o.Kind, o.Name, o.Payload)
			if err != nil {
				result[o.Kind] = "失败：" + err.Error()
				continue
			}
			result[o.Kind] = act
		}
		writeJSON(w, map[string]any{"ok": true, "imported": result})
	})
}

func (a *ImportAPI) auth(r *http.Request) bool {
	if a.Auth == nil {
		return true
	}
	return a.Auth.Check(r)
}

/* ---------- 备份压缩包解析 ---------- */

// importBackup 解析 ClassIsland 备份压缩包并写入各类档案。
// 返回 类型 -> 处理结果 的映射。
func importBackup(st *store.Store, data []byte) (map[string]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("无法读取压缩包：%v", err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || strings.HasSuffix(f.Name, ".bak") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		// 压缩包里也可能放 YAML 档案，统一转成 JSON 便于后续处理
		if isYAMLName(f.Name) {
			if conv, cerr := yamlToJSON(b); cerr == nil {
				b = conv
			}
		}
		// Windows 生成的压缩包里目录分隔符可能是反斜杠，这里统一成正斜杠
		files[strings.ReplaceAll(f.Name, "\\", "/")] = b
	}

	// 定位档案文件（Profiles 目录下任意非 .bak 的 json）
	var profileRaw []byte
	for name, b := range files {
		if strings.HasPrefix(name, "Profiles/") &&
			(strings.HasSuffix(name, ".json") || isYAMLName(name)) {
			profileRaw = b
			break
		}
	}
	result := map[string]string{}
	if profileRaw == nil {
		names := make([]string, 0, len(files))
		for n := range files {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) > 15 {
			names = names[:15]
		}
		return nil, fmt.Errorf("压缩包里没有找到档案文件，包内条目：%v", names)
	}

	var profile map[string]json.RawMessage
	if err := json.Unmarshal(profileRaw, &profile); err != nil {
		return nil, fmt.Errorf("档案文件解析失败：%v", err)
	}

	// 重要：三个档案保存的是同一份「完整档案」，而不是各自只存自己那部分。
	// 客户端分别加载 subjects / timelayouts / classplans，只有每个文件里都同时包含
	// 科目、时间表、课表三类数据时，课表才能找到对应的时间点和科目名并正确显示。
	full, err := json.Marshal(profile)
	if err != nil {
		return nil, fmt.Errorf("档案序列化失败：%v", err)
	}
	for _, k := range []struct {
		kind string
		name string
	}{
		{string(model.KindSubject), "科目档案"},
		{string(model.KindTimeLayout), "时间表档案"},
		{string(model.KindClassPlan), "课表档案"},
	} {
		act, err := upsertObject(st, k.kind, k.name, string(full))
		if err != nil {
			result[k.kind] = "失败：" + err.Error()
			continue
		}
		result[k.kind] = act
	}

	// 可选文件：策略 / 设置 / 组件布局
	extras := []struct {
		kind string
		path string
		name string
	}{
		{string(model.KindPolicy), "Config/Management/Policy.json", "策略档案"},
		{string(model.KindSettings), "Settings.json", "默认设置档案"},
		{string(model.KindComponents), "Config/ComponentLayouts/Default.json", "组件布局档案"},
	}
	for _, e := range extras {
		b, ok := files[e.path]
		if !ok {
			// 组件布局文件名可能不同，退而求其次取目录下第一个
			for name, v := range files {
				if strings.HasPrefix(name, "Config/ComponentLayouts/") && strings.HasSuffix(name, ".json") {
					b, ok = v, true
					break
				}
			}
		}
		if !ok {
			continue
		}
		if _, err := upsertObject(st, e.kind, e.name, string(b)); err != nil {
			result[e.kind] = "失败：" + err.Error()
		} else {
			result[e.kind] = "已导入"
		}
	}
	return result, nil
}

// splitProfile 把一个完整档案按类型拆成只含该类型字典的档案
func splitProfile(profile map[string]json.RawMessage, key string) (string, error) {
	dictKeys := map[string]bool{"Subjects": true, "TimeLayouts": true, "ClassPlans": true,
		"ClassPlanGroups": true, "SelectedClassPlanGroupId": true, "TempClassPlanGroupId": true}

	out := map[string]json.RawMessage{}
	for k, v := range profile {
		if dictKeys[k] {
			continue
		}
		out[k] = v
	}
	// 分组相关字段只随课表档案保存
	if key == "ClassPlans" {
		for _, g := range []string{"ClassPlanGroups", "SelectedClassPlanGroupId", "TempClassPlanGroupId"} {
			if v, ok := profile[g]; ok {
				out[g] = v
			}
		}
	}
	for _, k := range []string{"Subjects", "TimeLayouts", "ClassPlans"} {
		if k == key {
			if v, ok := profile[k]; ok {
				out[k] = v
				continue
			}
			out[k] = json.RawMessage("{}")
			continue
		}
		out[k] = json.RawMessage("{}")
	}
	b, err := json.Marshal(out)
	return string(b), err
}

/* ---------- YAML 支持 ---------- */

// isYAMLName 判断文件名是否为 YAML
func isYAMLName(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml")
}

// yamlToJSON 把 YAML 转成 JSON。
// YAML 的映射键可能是非字符串（如数字），这里递归字符串化，保证能序列化为 JSON。
func yamlToJSON(data []byte) ([]byte, error) {
	var v interface{}
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.Marshal(normalizeYAMLValue(v))
}

func normalizeYAMLValue(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			out[k] = normalizeYAMLValue(val)
		}
		return out
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			out[fmt.Sprint(k)] = normalizeYAMLValue(val)
		}
		return out
	case []interface{}:
		for i, val := range x {
			x[i] = normalizeYAMLValue(val)
		}
		return x
	default:
		return v
	}
}

// normalizeProfile 处理用户直接上传的档案文件（JSON 或 YAML）：
// 若已是完整档案结构则提取对应字典，否则原样保存。
func normalizeProfile(kind, name string, data []byte) (string, error) {
	// YAML：先转成 JSON 再走同一套逻辑
	if isYAMLName(name) {
		converted, err := yamlToJSON(data)
		if err != nil {
			return "", fmt.Errorf("YAML 解析失败：%v", err)
		}
		data = converted
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		// 不是 JSON 时再尝试一次 YAML（用户可能用了不规范的扩展名）
		converted, yerr := yamlToJSON(data)
		if yerr != nil {
			return "", fmt.Errorf("既不是合法 JSON 也不是合法 YAML：%v", err)
		}
		data = converted
		if err := json.Unmarshal(data, &raw); err != nil {
			return "", fmt.Errorf("解析失败：%v", err)
		}
	}
	// 原样保存完整档案：客户端需要每个文件里都带有科目/时间表/课表三类数据
	return string(data), nil
}

// upsertObject 已存在则覆盖（版本 +1），否则新建
func upsertObject(st *store.Store, kind, name, payload string) (string, error) {
	list, err := st.ListObjects(kind)
	if err != nil {
		return "", err
	}
	if len(list) > 0 {
		if _, err := st.UpdateObjectPayload(list[0].ID, payload); err != nil {
			return "", err
		}
		return "更新（版本 +1）", nil
	}
	if _, err := st.CreateObject(kind, name, payload); err != nil {
		return "", err
	}
	return "新建", nil
}
