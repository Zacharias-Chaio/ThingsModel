package store

// 属性点位模式：physical 实际点位（绑定网关来源），logical 逻辑点位（值存内存缓存，可被服务写入）。
const (
	PropertyModePhysical = "physical"
	PropertyModeLogical  = "logical"
)

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
	Number      int          `json:"number"`      // 属性编号 0,1,2,3...（自动生成，不可修改）
	Name        string       `json:"name"`        // 中文属性名称
	Key         string       `json:"key"`         // 英文属性名称（标识符）
	Default     any          `json:"default"`     // 属性默认值（可为 null 表示不设置）
	Unit        string       `json:"unit"`        // 属性单位（选填）
	Type        string       `json:"type"`        // status（状态类型）| number（数据类型）
	Description []StatusDesc `json:"description"` // 状态值描述（type=status 时有效）
	Mode        string       `json:"mode"`        // 点位模式：physical（实际点位，默认）| logical（逻辑点位）
	Binding     Binding      `json:"binding"`     // 属性绑定（聚合方法 + 数据来源；logical 时 Sources 为空）
}

// StatusDesc 状态值描述（属性/服务的 status 类型）。
type StatusDesc struct {
	Enum  int    `json:"enum"`  // 状态值（不可修改；-999 约定为未知状态）
	Key   string `json:"key"`   // 状态标签
	Name  string `json:"name"`  // 状态名称
	Value any    `json:"value"` // 工程值（属性可为空；服务必填且唯一）
}

// Binding 属性绑定：聚合方法 + 多个数据来源。
// Method 取值：ept(直接绑定)、sum、avg、min、max、and、or、not（统一小写，默认 ept）。
type Binding struct {
	Method  string          `json:"method"`  // ept, sum, avg, min, max, and, or, not（默认 ept）
	Sources []BindingSource `json:"sources"` // 数据来源列表
}

// BindingSource 绑定来源：网关/通道/设备/属性 四级定位一个上游点位。
// ChannelID、DeviceID 为上游网关内的索引（从 0 开始）；来源允许留空（未绑定）。
type BindingSource struct {
	GatewayID  string `json:"gatewayId"`  // 网关 ID
	ChannelID  int    `json:"channelId"`  // 通道 ID（通道索引）
	DeviceID   int    `json:"deviceId"`   // 设备 ID（设备索引）
	PropertyID string `json:"propertyId"` // 属性 ID
}

// Method 服务/方法定义。
// 注：模板说明.md 中 description 同时用作服务描述(字符串)与状态值列表(数组)，
// 此处拆为两个字段以避免 JSON 键冲突：Desc 为描述文本，Descriptions 为状态值。
type Method struct {
	Number       int           `json:"number"`       // 服务编号 0,1,2,3...（自动生成，不可修改）
	Name         string        `json:"name"`         // 服务名称
	Key          string        `json:"key"`          // 服务英文名称（标识符）
	Desc         string        `json:"desc"`         // 服务描述（文本）
	Type         string        `json:"type"`         // status（状态类型）| number（数值类型）
	Validation   Validation    `json:"validation"`   // 有效性检查（数值类型时配置最小/最大值）
	Descriptions []StatusDesc  `json:"descriptions"` // 状态值描述（type=status 时有效，工程值必填且唯一）
	Binding      MethodBinding `json:"binding"`      // 控制绑定（单点下发）
}

// Validation 数值有效性检查。
type Validation struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// MethodBinding 方法绑定：引用目标属性点位（该设备模板属性 key）。
// 目标属性为 physical 时值下发到其绑定来源；logical 时写入内存缓存并北向发布。
type MethodBinding struct {
	PropertyKey string `json:"propertyKey"`
}

// Event 告警规则定义（模板层，纯定义，不含绑定）。
type Event struct {
	Number      int     `json:"number"`      // 事件编号
	Name        string  `json:"name"`        // 事件名称
	Key         string  `json:"key"`         // 事件英文名称
	Description string  `json:"description"` // 事件描述
	Level       int     `json:"level"`       // 0-提示 1-一般 2-严重 3-紧急
	Type        string  `json:"type"`        // equal | upper | lower
	Threshold   float64 `json:"threshold"`   // 触发阈值
	Time        int     `json:"time"`        // 持续时间（毫秒），0 表示立即触发
}

// AlarmBinding 告警监测点位关联：一个属性点位匹配多条告警规则。
// 单条规则 method=ept 直判；多条规则 method=and（全部成立）/ or（任一成立，默认）。
type AlarmBinding struct {
	PropertyKey string   `json:"propertyKey"` // 监测的属性点位 key（该设备模板属性）
	Method      string   `json:"method"`      // ept | and | or
	Rules       []string `json:"rules"`       // 关联的告警规则 key 列表
}

// DeviceEvents 设备实例的告警配置：规则快照 + 检测点位关联。
type DeviceEvents struct {
	Rule    []Event        `json:"rule"`    // 告警规则（模板快照，纯定义）
	Binding []AlarmBinding `json:"binding"` // 检测点位与告警规则的关联
}
