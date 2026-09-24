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
	MessageTypeData   = "data"   // 遥测上行（网关 → NATS）
	MessageTypeCmd    = "cmd"    // 控制下行（NATS → 网关，REQ/REP）
	MessageTypeCmdAck = "cmdAck" // 控制执行结果（网关复用 .data 主题异步回报）
	MessageTypeQuery  = "query"  // 拓扑查询（NATS → 网关，REQ/REP）
	QueryTypeTopology = "queryTopology"
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

// NormalizedData is the ThingsModel fan-out payload published to its .data subject.
type NormalizedData struct {
	ThingsModelID   string                        `json:"thingsmodel_id"`
	DeviceID        string                        `json:"device_id"`
	DeviceName      string                        `json:"device_name"`
	TemplateCode    string                        `json:"template_code"`
	TemplateVersion string                        `json:"template_version"`
	DataStatus      string                        `json:"data_status"`
	Properties      map[string]NormalizedProperty `json:"properties"`
	Events          map[string]NormalizedEvent    `json:"events"`
	Timestamp       int64                         `json:"timestamp"`
}

type NormalizedProperty struct {
	Name      string `json:"name"`
	Unit      string `json:"unit"`
	Value     any    `json:"value"`
	Timestamp int64  `json:"timestamp"`
	Quality   string `json:"quality"`
}

type NormalizedEvent struct {
	Name      string `json:"name"`
	Level     int    `json:"level"`
	Status    string `json:"status"`
	Active    bool   `json:"active"`
	Timestamp int64  `json:"timestamp"`
}
