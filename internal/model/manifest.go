// Package model 定义集控清单与档案相关的模型。
// 字段使用 PascalCase，以匹配 ClassIsland 客户端（System.Text.Json）的属性命名。
package model

// ServerKind 集控服务器类型
const (
	ServerKindServerless       = 0 // 静态托管的配置文件
	ServerKindManagementServer = 1 // 集控服务器
)

// CoreVersion 集控核心版本，客户端会校验主版本号必须一致。
const CoreVersion = "2.0.0.0"

// ReVersionString 存储了字符串和其版本信息的类型。
// 客户端只在 Version 变大时才重新拉取 Value 指向的内容。
type ReVersionString struct {
	Version int    `json:"Version"`
	Value   string `json:"Value,omitempty"`
}

// ManagementManifest 集控清单，客户端每次启动都会拉取。
type ManagementManifest struct {
	ClassPlanSource       ReVersionString `json:"ClassPlanSource"`
	TimeLayoutSource      ReVersionString `json:"TimeLayoutSource"`
	SubjectsSource        ReVersionString `json:"SubjectsSource"`
	DefaultSettingsSource ReVersionString `json:"DefaultSettingsSource"`
	PolicySource          ReVersionString `json:"PolicySource"`
	ComponentsSource      ReVersionString `json:"ComponentsSource"`
	CredentialSource      ReVersionString `json:"CredentialSource"`

	ServerKind       int    `json:"ServerKind"`
	OrganizationName string `json:"OrganizationName"`
	CoreVersion      string `json:"CoreVersion"`
}

// ManagementSettings 客户端集控配置（ManagementPreset.json 的结构）。
type ManagementSettings struct {
	ManagementServerKind int    `json:"ManagementServerKind"`
	ManagementServer     string `json:"ManagementServer"`
	ManagementServerGrpc string `json:"ManagementServerGrpc"`
	ManifestUrlTemplate  string `json:"ManifestUrlTemplate,omitempty"`
	ClassIdentity        string `json:"ClassIdentity,omitempty"`
}

// ObjectKind 档案对象类型
type ObjectKind string

const (
	KindClassPlan   ObjectKind = "classplan"
	KindTimeLayout  ObjectKind = "timelayout"
	KindSubject     ObjectKind = "subject"
	KindPolicy      ObjectKind = "policy"
	KindSettings    ObjectKind = "settings"
	KindComponents  ObjectKind = "components"
	KindCredentials ObjectKind = "credentials"
)

// Kinds 全部档案类型，用于遍历分发。
var Kinds = []ObjectKind{KindClassPlan, KindTimeLayout, KindSubject, KindPolicy, KindSettings, KindComponents, KindCredentials}

// Valid 判断档案类型是否合法
func (k ObjectKind) Valid() bool {
	for _, v := range Kinds {
		if v == k {
			return true
		}
	}
	return false
}
