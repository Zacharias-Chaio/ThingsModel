package natsclient

import (
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

const (
	headerMessageID        = "PP-Message-ID"
	headerMessageType      = "PP-Message-Type"
	headerMessageVersion   = "PP-Message-Version"
	headerMessageTimestamp = "PP-Message-Timestamp"
	messageVersion         = "v1.0"
)

type envelope struct {
	ID        string
	Type      string
	Version   string
	Timestamp time.Time
	Payload   []byte
}

func newEnvelope(messageType string, payload []byte) envelope {
	return envelope{ID: uuid.NewString(), Type: messageType, Version: messageVersion, Timestamp: time.Now().UTC(), Payload: payload}
}

func envelopeFromMsg(msg *nats.Msg) (envelope, error) {
	if msg.Header == nil {
		return envelope{}, fmt.Errorf("缺少消息头")
	}
	timestamp, err := strconv.ParseInt(msg.Header.Get(headerMessageTimestamp), 10, 64)
	if err != nil {
		return envelope{}, fmt.Errorf("无效消息时间戳: %w", err)
	}
	parsed := envelope{
		ID: msg.Header.Get(headerMessageID), Type: msg.Header.Get(headerMessageType),
		Version: msg.Header.Get(headerMessageVersion), Timestamp: time.UnixMilli(timestamp), Payload: msg.Data,
	}
	if parsed.ID == "" || parsed.Type == "" || parsed.Version == "" {
		return envelope{}, fmt.Errorf("消息头缺少必填字段")
	}
	return parsed, nil
}

func (e envelope) toMsg(subject string) *nats.Msg {
	message := nats.NewMsg(subject)
	message.Header.Set(headerMessageID, e.ID)
	message.Header.Set(headerMessageType, e.Type)
	message.Header.Set(headerMessageVersion, e.Version)
	message.Header.Set(headerMessageTimestamp, strconv.FormatInt(e.Timestamp.UnixMilli(), 10))
	message.Data = e.Payload
	return message
}

// MessageData is the Gateway-Modbus telemetry payload published to *.data.
type MessageData struct {
	GatewayID    string             `json:"gateway_id"`
	GatewaySN    string             `json:"gateway_sn"`
	ChannelIndex int                `json:"channel_index"`
	DeviceIndex  int                `json:"device_index"`
	DeviceName   string             `json:"device_name"`
	CommNo       int                `json:"comm_no"`
	ModelID      string             `json:"model_id"`
	ModelName    string             `json:"model_name"`
	Properties   map[string]PropVal `json:"properties"`
}

type PropVal struct {
	Name        string `json:"name"`
	Unit        string `json:"unit"`
	Description string `json:"description"`
	AccessMode  string `json:"access_mode"`
	Value       any    `json:"value"`
	Timestamp   int64  `json:"timestamp"`
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
