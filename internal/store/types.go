package store

// 模板数据结构，严格遵循 templats/模板说明.md 设计。
//
// 顶层 Template 对应一个 .json 模板文件，存放在 templats/ 目录。
// 文件名约定：{Code}.json 或 {Name}.json，由 store 层管理。

// Template 物模型模板顶层结构。
type Template struct {
	Name        string     `json:"name"`        // 模板名称
	Code        string     `json:"code"`        // 模板编号
	Category    string     `json:"category"`    // 模板分类
	Version     string     `json:"version"`     // 模板版本
	Description string     `json:"description"` // 模板描述
	Properties  []Property `json:"properties"`  // 属性定义列表
	Methods     []Method   `json:"methods"`     // 服务/方法定义列表
	Events      []Event    `json:"events"`      // 告警/事件定义列表
}

// Property 属性定义。
type Property struct {
	Number      int        `json:"number"`      // 属性编号 0,1,2,3...
	Name        string     `json:"name"`        // 中文属性名称
	Key         string     `json:"key"`         // 英文属性名称
	Default     any        `json:"default"`     // 属性默认值
	Unit        string     `json:"unit"`        // 属性单位
	Type        string     `json:"type"`        // enum | number
	Description []EnumDesc `json:"description"` // 枚举值描述（type=enum 时有效）
	Binding     Binding    `json:"binding"`     // 属性绑定（聚合方法 + 数据来源）
}

// EnumDesc 枚举值描述。
type EnumDesc struct {
	Name   string `json:"name"`   // 中文名称
	Key    string `json:"key"`    // 英文标识
	Status int    `json:"status"` // 状态枚举值（不可修改）
	Value  any    `json:"value"`  // 实际采集/下发值
}

// Binding 属性绑定：聚合方法 + 多个数据来源。
// Method 取值：EPT(直接绑定)、SUM、AVG、MIN、MAX、AND、OR、NOT。
type Binding struct {
	Method  string          `json:"method"`  // EPT, SUM, AVG, MIN, MAX, AND, OR, NOT
	Sources []BindingSource `json:"sources"` // 数据来源列表
}

// BindingSource 绑定来源：设备ID + 属性ID。
type BindingSource struct {
	DeviceID   string `json:"deviceId"`
	PropertyID string `json:"propertyId"`
}

// Method 服务/方法定义。
// 注：模板说明.md 中 description 同时用作方法描述(字符串)与枚举值列表(数组)，
// 此处拆为两个字段以避免 JSON 键冲突：Desc 为描述文本，Descriptions 为枚举值。
type Method struct {
	Number       int           `json:"number"`       // 方法编号
	Name         string        `json:"name"`         // 方法名称
	Key          string        `json:"key"`          // 方法英文名称
	Desc         string        `json:"desc"`         // 方法描述（文本）
	Type         string        `json:"type"`         // enum | number
	Validation   Validation    `json:"validation"`   // 有效性检查（数值类型必填）
	Descriptions []EnumDesc    `json:"descriptions"` // 枚举值描述（type=enum 时有效）
	Binding      MethodBinding `json:"binding"`      // 控制绑定（单点下发）
}

// Validation 数值有效性检查。
type Validation struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// MethodBinding 方法绑定：单点下发到指定设备的指定属性。
type MethodBinding struct {
	DeviceID   string `json:"deviceId"`
	PropertyID string `json:"propertyId"`
}

// Event 告警/事件定义。
type Event struct {
	Number      int             `json:"number"`      // 事件编号
	Name        string          `json:"name"`        // 事件名称
	Key         string          `json:"key"`         // 事件英文名称
	Description string          `json:"description"` // 事件描述
	Level       int             `json:"level"`       // 0-提示 1-一般 2-严重 3-紧急
	Type        string          `json:"type"`        // equal | upper | lower
	Threshold   float64         `json:"threshold"`   // 触发阈值
	Time        int             `json:"time"`        // 持续时间（秒），0 表示立即触发
	Binding     []BindingSource `json:"binding"`     // 关联属性列表
}
