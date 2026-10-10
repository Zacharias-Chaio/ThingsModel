// message.go - 消息契约：信封与各主题载体定义，对齐网关侧 message-contract-design.md。
package natsclient

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// 信封 Header 契约（与网关侧一致）。
const (
	HeaderMessageID        = "PP-Message-ID"        // 消息唯一 ID，UUIDv4
	HeaderMessageType      = "PP-Message-Type"      // 消息类型：data / cmd / cmdAck / query
	HeaderMessageVersion   = "PP-Message-Version"   // 消息版本号
	HeaderMessageTimestamp = "PP-Message-Timestamp" // 消息时间戳，Unix 毫秒
	MessageVersion         = "v1.0"
)

// 信封消息类型（PP-Message-Type 取值）。
const (
	MessageTypeData   = "data"   // 遥测上行（网关 → NATS）与北向数据发布
	MessageTypeCmd    = "cmd"    // 控制下行（NATS → 网关，REQ/REP）
	MessageTypeCmdAck = "cmdAck" // 控制执行结果（网关复用 .data 主题异步回报）
	MessageTypeQuery  = "query"  // 拓扑查询（NATS → 网关）与北向查询（下游 → 物模型，REQ/REP）
	MessageTypeAlarm  = "alarm"  // 标准化告警/事件（物模型 → 下游，PUB/SUB）
	MessageTypeControl = "control" // 服务控制（下游 → 物模型，REQ/REP）
	QueryTypeTopology = "queryTopology"
)

// 北向主题：固定常量，无实例 ID（单例部署，实例身份在负载中）。
const (
	SubjectData            = "powerpulse.thingsmodel.data"
	SubjectAlarm           = "powerpulse.alarm.thingsmodel"
	SubjectQueryDevices    = "powerpulse.thingsmodel.query.devices"
	SubjectQueryProperties = "powerpulse.thingsmodel.query.properties"
	SubjectQueryMethods    = "powerpulse.thingsmodel.query.methods"
	SubjectQueryEvents     = "powerpulse.thingsmodel.query.events"
	SubjectControl         = "powerpulse.thingsmodel.cmd"
)

// 消息执行状态（MessageAck.Status 取值）。
const (
	AckStatusAccepted = "accepted"
	AckStatusFailure  = "failure"
	AckStatusSuccess  = "success"
)

// Envelope 是 NATS 消息信封：元数据映射到 msg.Header，业务负载映射到 msg.Data。
type Envelope struct {
	ID        string
	Type      string
	Version   string
	Timestamp time.Time
	Payload   []byte
}

func NewEnvelope(messageType string, payload []byte) Envelope {
	return Envelope{ID: uuid.NewString(), Type: messageType, Version: MessageVersion, Timestamp: time.Now().UTC(), Payload: payload}
}

func envelopeFromMsg(msg *nats.Msg) (Envelope, error) {
	if msg.Header == nil {
		return Envelope{}, fmt.Errorf("缺少消息头")
	}
	timestamp, err := strconv.ParseInt(msg.Header.Get(HeaderMessageTimestamp), 10, 64)
	if err != nil {
		return Envelope{}, fmt.Errorf("无效消息时间戳: %w", err)
	}
	parsed := Envelope{
		ID: msg.Header.Get(HeaderMessageID), Type: msg.Header.Get(HeaderMessageType),
		Version: msg.Header.Get(HeaderMessageVersion), Timestamp: time.UnixMilli(timestamp), Payload: msg.Data,
	}
	if parsed.ID == "" || parsed.Type == "" || parsed.Version == "" {
		return Envelope{}, fmt.Errorf("消息头缺少必填字段")
	}
	return parsed, nil
}

func (e Envelope) toMsg(subject string) *nats.Msg {
	message := nats.NewMsg(subject)
	message.Header.Set(HeaderMessageID, e.ID)
	message.Header.Set(HeaderMessageType, e.Type)
	message.Header.Set(HeaderMessageVersion, e.Version)
	message.Header.Set(HeaderMessageTimestamp, strconv.FormatInt(e.Timestamp.UnixMilli(), 10))
	message.Data = e.Payload
	return message
}

// MessageData 是 Gateway-Modbus 发布到 {prefix}.{gateway}.data 的遥测载体（全量刷新）。
type MessageData struct {
	GatewayID    string             `json:"gateway_id"`
	GatewaySN    string             `json:"gateway_sn"`
	ChannelIndex int                `json:"channel_index"`
	DeviceIndex  int                `json:"device_index"`
	DeviceName   string             `json:"device_name"`
	DeviceCode   string             `json:"device_code"` // 设备唯一编码：{gateway_id}{通道索引:03d}{设备索引:03d}
	CommNo       int                `json:"comm_no"`
	ModelID      string             `json:"model_id"`
	ModelName    string             `json:"model_name"`
	Properties   map[string]PropVal `json:"properties"`
}

// PropVal 单个属性值。Value 为 null 表示本轮采集解析异常，不使用历史缓存。
type PropVal struct {
	Index       int    `json:"index"` // 属性索引（模型属性列表中的序号，从 0 开始）
	Name        string `json:"name"`
	Unit        string `json:"unit"`
	Description string `json:"description"`
	AccessMode  string `json:"access_mode"`
	Value       any    `json:"value"`
	Timestamp   int64  `json:"timestamp"`
}

// MessageCmd 是发往 {prefix}.{gateway}.cmd 的单条写命令请求（REQ/REP）。
type MessageCmd struct {
	ChannelIndex int     `json:"channel_index"`
	DeviceIndex  int     `json:"device_index"`
	Name         string  `json:"name"`  // 属性名称
	Value        float64 `json:"value"` // 工程值
}

// MessageAck 是写命令应答：REQ/REP 同步回复 accepted/failure；
// 实际执行完成后网关复用 .data 主题以 type=cmdAck 异步回报 success/failure。
type MessageAck struct {
	RequestID    string `json:"request_id"` // 原请求 PP-Message-ID
	ChannelIndex int    `json:"channel_index"`
	DeviceIndex  int    `json:"device_index"`
	Status       string `json:"status"` // accepted / failure / success
	Message      string `json:"message,omitempty"`
}

// MessageQuery 是发往 {prefix}.{gateway}.query 的查询请求（REQ/REP）。
type MessageQuery struct {
	Type string          `json:"type"`           // 当前仅 queryTopology
	Body json.RawMessage `json:"body,omitempty"` // 查询参数（预留）
}

// MessageQueryResp 是拓扑查询响应：一次返回全量拓扑，不含实时值。
type MessageQueryResp struct {
	Type     string        `json:"type"`
	OK       bool          `json:"ok"`
	Message  string        `json:"message,omitempty"`
	Channels []ChannelInfo `json:"channels"`
}

// ChannelInfo 通道信息。
type ChannelInfo struct {
	ID           string       `json:"id"` // 格式 Channel-{通道索引}
	ChannelIndex int          `json:"channel_index"`
	Name         string       `json:"name"`
	Type         string       `json:"type"` // Serial / Network
	Connected    bool         `json:"connected"`
	Devices      []DeviceInfo `json:"devices"`
}

// DeviceInfo 挂载设备信息：标识 + 数据点表。
type DeviceInfo struct {
	Index     int         `json:"index"` // 即 data/cmd 消息中的 device_index
	Name      string      `json:"name"`
	Datasheet []DataEntry `json:"datasheet"`
}

// DataEntry 单个数据点（属性）的元数据。
type DataEntry struct {
	DataIndex string `json:"data_id"`
	DataName  string `json:"data_name"`
	DataRW    string `json:"data_rw"` // R / W / RW
}

// NormalizedData 设备数据发布载体（周期发布，契约 §3.3）。
type NormalizedData struct {
	ThingsModelID   string                        `json:"thingsmodel_id"`
	DeviceID        string                        `json:"device_id"`
	DeviceName      string                        `json:"device_name"`
	TemplateCode    string                        `json:"template_code"`
	TemplateVersion string                        `json:"template_version"`
	DataStatus      string                        `json:"data_status"`     // available / degraded / unavailable
	ConfigRevision  int64                         `json:"config_revision"` // 设备配置版本号：变化=定义已变更，应重查定义
	Properties      map[string]NormalizedProperty `json:"properties"`      // 属性 key → 归一化值
	Events          map[string]NormalizedEvent    `json:"events"`          // 告警 key → 实时状态
	Seq             uint64                        `json:"seq"`             // 发布序列号（设备内单调递增）：跳号=有丢失
	Timestamp       int64                         `json:"timestamp"`
}

// NormalizedProperty 归一化后的单个属性值。
type NormalizedProperty struct {
	ID        string `json:"id,omitempty"` // 实例条目 ID（property_N）
	Name      string `json:"name"`
	Unit      string `json:"unit"`
	Value     any    `json:"value"`
	Timestamp int64  `json:"timestamp"` // 最近一次来源时间，Unix 毫秒；0 表示无时间来源
	Quality   string `json:"quality"`
}

// NormalizedEvent 单条告警的实时状态（定义见 .query.events）。
type NormalizedEvent struct {
	ID        string `json:"id,omitempty"` // 实例条目 ID（event_N）
	Name      string `json:"name"`
	Level     int    `json:"level"`
	Status    string `json:"status"` // active / pending / inactive / unavailable / unbound
	Active    bool   `json:"active"`
	Value     any    `json:"value"`      // 监测点位当前归一化值（无数据为 null）
	Point     string `json:"point"`      // 监测属性 key
	PointName string `json:"point_name"` // 监测属性名称
	Timestamp int64  `json:"timestamp"`
}

// ─── 平台告警标准（docs/powerpulse-alarm-standard.md）───

// 告警类别与生命周期取值。
const (
	AlarmCategoryDevice   = "device"
	AlarmCategorySoftware = "software"
	AlarmCategorySystem   = "system"

	AlarmKindTrigger = "trigger"
	AlarmKindRecover = "recover"
	AlarmKindEvent   = "event"
)

// 已登记事件码（平台标准第 6 节）。
const (
	AlarmCodeCmdResult          = "thingsmodel.cmd.result"
	AlarmCodeCmdFrequentFailure = "thingsmodel.cmd.frequent_failure"
	AlarmCodeModelChanged       = "thingsmodel.model.changed"
)

// AlarmMessage 平台通用告警/事件消息。
type AlarmMessage struct {
	AlarmID   string      `json:"alarm_id"`        // UUIDv4，消费方幂等键
	Source    AlarmSource `json:"source"`          // 发布方来源
	Category  string      `json:"category"`        // device | software | system
	Kind      string      `json:"kind"`            // trigger | recover | event
	Level     int         `json:"level"`           // 0 提示 / 1 一般 / 2 严重 / 3 紧急
	Timestamp int64       `json:"timestamp"`       // Unix 毫秒
	Count     int         `json:"count,omitempty"` // 节流聚合计数（聚合消息 >1）

	Device   *AlarmDevice   `json:"device,omitempty"`   // category=device 时必填
	Software *AlarmSoftware `json:"software,omitempty"` // category=software 时必填
	System   *AlarmSystem   `json:"system,omitempty"`   // category=system 时必填
}

// AlarmSource 发布方来源。
type AlarmSource struct {
	Module   string `json:"module"`   // 模块名，与主题二级命名空间一致
	Instance string `json:"instance"` // 模块内实例标识
}

// AlarmDevice 设备告警明细：由设备监测点位规则触发。
type AlarmDevice struct {
	DeviceID   string    `json:"device_id"`
	DeviceName string    `json:"device_name"`
	EventID    string    `json:"event_id,omitempty"` // 告警条目 ID（event_N）
	EventKey   string    `json:"event_key"`
	EventName  string    `json:"event_name"`
	Point      string    `json:"point"`       // 监测属性 key
	PointName  string    `json:"point_name"`  // 监测属性名称
	Value      any       `json:"value"`       // 触发值
	Rule       AlarmRule `json:"rule"`        // 判定依据
}

// AlarmRule 告警判定规则。
type AlarmRule struct {
	Type      string  `json:"type"`      // equal | upper | lower
	Threshold float64 `json:"threshold"` // 阈值
	Time      int     `json:"time"`      // 持续时间（毫秒），0 表示立即触发
}

// AlarmSoftware 软件告警明细：模块自身行为异常。
type AlarmSoftware struct {
	Code      string         `json:"code"`
	Component string         `json:"component"`
	Message   string         `json:"message,omitempty"`
	Context   map[string]any `json:"context,omitempty"`
}

// AlarmSystem 系统告警明细：跨模块/平台级事件（预留）。
type AlarmSystem struct {
	Code      string         `json:"code"`
	Component string         `json:"component"`
	Message   string         `json:"message,omitempty"`
	Context   map[string]any `json:"context,omitempty"`
}

// ─── 北向查询契约（契约 §5）───

// DevicesQueryResponse 设备清单查询响应（请求无参数，载体为空 JSON 对象）。
type DevicesQueryResponse struct {
	OK      bool            `json:"ok"`
	Message string          `json:"message,omitempty"`
	Devices []DeviceSummary `json:"devices"`
}

// DeviceSummary 设备实例概要。
type DeviceSummary struct {
	DeviceID        string `json:"device_id"`
	DeviceName      string `json:"device_name"`
	TemplateCode    string `json:"template_code"`
	TemplateVersion string `json:"template_version,omitempty"`
	Description     string `json:"description,omitempty"`
	Enabled         bool   `json:"enabled"`
	DataStatus      string `json:"data_status,omitempty"`
	PropertyCount   int    `json:"property_count"`
	MethodCount     int    `json:"method_count"`
	EventCount      int    `json:"event_count"`
}

// PropertiesQueryRequest 属性定义查询请求。
type PropertiesQueryRequest struct {
	DeviceID string `json:"device_id"`
}

// PropertiesQueryResponse 属性定义查询响应。
type PropertiesQueryResponse struct {
	OK         bool                 `json:"ok"`
	Message    string               `json:"message,omitempty"`
	DeviceID   string               `json:"device_id"`
	Properties []PropertyDefinition `json:"properties"`
}

// PropertyDefinition 单个属性的定义。
type PropertyDefinition struct {
	ID           string            `json:"id"`                     // 实例条目 ID（property_N）
	Key          string            `json:"key"`
	Name         string            `json:"name"`
	Unit         string            `json:"unit,omitempty"`
	Type         string            `json:"type"`                   // status | number
	Mode         string            `json:"mode"`                   // physical | logical
	Method       string            `json:"method"`                 // ept / sum / avg / min / max / and / or / not
	Default      any               `json:"default,omitempty"`
	Descriptions []StatusDefinition `json:"descriptions,omitempty"` // type=status 时：状态值
}

// MethodsQueryRequest 服务定义查询请求。
type MethodsQueryRequest struct {
	DeviceID string `json:"device_id"`
}

// MethodsQueryResponse 服务定义查询响应。
type MethodsQueryResponse struct {
	OK      bool               `json:"ok"`
	Message string             `json:"message,omitempty"`
	DeviceID string             `json:"device_id"`
	Methods []MethodDefinition `json:"methods"`
}

// MethodDefinition 单个服务（控制能力）的定义。
type MethodDefinition struct {
	ID           string            `json:"id"`                     // 实例条目 ID（method_N）
	Key          string            `json:"key"`
	Name         string            `json:"name"`
	Type         string            `json:"type"`                  // status | number
	Bound        bool              `json:"bound"`                 // 是否已绑定目标点位（false 不可控制）
	TargetMode   string            `json:"target_mode,omitempty"` // physical | logical（bound 时给出）
	Validation   *RangeValidation  `json:"validation,omitempty"`  // type=number：工程值范围
	Descriptions []StatusDefinition `json:"descriptions,omitempty"` // type=status：合法工程值枚举
}

// EventsQueryRequest 告警定义查询请求。
type EventsQueryRequest struct {
	DeviceID string `json:"device_id"`
}

// EventsQueryResponse 告警定义查询响应。
type EventsQueryResponse struct {
	OK      bool              `json:"ok"`
	Message string            `json:"message,omitempty"`
	DeviceID string            `json:"device_id"`
	Events  []EventDefinition `json:"events"`
}

// EventDefinition 单条告警的定义。
type EventDefinition struct {
	ID                  string  `json:"id"`                     // 实例条目 ID（event_N）
	Key                 string  `json:"key"`
	Name                string  `json:"name"`
	Description         string  `json:"description,omitempty"`
	Level               int     `json:"level"` // 0 提示 / 1 一般 / 2 严重 / 3 紧急
	Type                string  `json:"type"`  // equal | upper | lower
	Threshold           float64 `json:"threshold"`
	Time                int     `json:"time"` // 持续时间（毫秒）
	BindingProperty     string  `json:"binding_property"`      // 监测属性 key（空=未绑定）
	BindingPropertyName string  `json:"binding_property_name,omitempty"`
}

// StatusDefinition 状态值描述（北向契约，与模板定义同构）。
type StatusDefinition struct {
	Enum  int    `json:"enum"`
	Key   string `json:"key"`
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// RangeValidation 数值型服务的有效性范围。
type RangeValidation struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// ─── 北向控制契约（契约 §6）───

// ControlRequest 服务控制下发请求。
type ControlRequest struct {
	DeviceID   string  `json:"device_id"`
	ServiceKey string  `json:"service_key"`
	Value      float64 `json:"value"`
}

// ControlAck 服务控制受理应答（REQ/REP 同步返回；终态仅失败经告警主题异步发布）。
type ControlAck struct {
	RequestID  string `json:"request_id"` // 原请求 PP-Message-ID
	DeviceID   string `json:"device_id"`
	ServiceKey string `json:"service_key"`
	Status     string `json:"status"` // accepted / failure
	Message    string `json:"message,omitempty"`
}
