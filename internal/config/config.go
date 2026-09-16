// Package config owns the editable application settings persisted in the platform database.
package config

import (
	"fmt"
	"strings"

	"thingsmodel/internal/logging"
)

type App struct {
	Software            Software             `json:"software"`
	Logger              logging.LoggerConfig `json:"logger"`
	Subscriber          Subscriber           `json:"subscriber"`
	Publisher           Publisher            `json:"publisher"`
	StaleAfter          int                  `json:"staleAfter"`
	DeviceSubscriptions []DeviceSubscription `json:"deviceSubscriptions"`
}

type Software struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Subscriber 配置数据订阅客户端：只负责消费网关遥测。
type Subscriber struct {
	Enabled              bool   `json:"enabled"`
	URL                  string `json:"url"`
	Name                 string `json:"name"`
	InputSubjectPrefix   string `json:"inputSubjectPrefix"`
	ConnectTimeout       int    `json:"connectTimeout"`
	ReconnectWait        int    `json:"reconnectWait"`
	MaxReconnects        int    `json:"maxReconnects"`
	RetryOnFailedConnect bool   `json:"retryOnFailedConnect"`
	PingInterval         int    `json:"pingInterval"`
	MaxPingsOut          int    `json:"maxPingsOut"`
}

// Publisher 配置内容发布客户端：负责把归一化数据与业务消息发布给下游。
type Publisher struct {
	Enabled              bool   `json:"enabled"`
	URL                  string `json:"url"`
	Name                 string `json:"name"`
	QueueSize            int    `json:"queueSize"`
	ConnectTimeout       int    `json:"connectTimeout"`
	ReconnectWait        int    `json:"reconnectWait"`
	MaxReconnects        int    `json:"maxReconnects"`
	RetryOnFailedConnect bool   `json:"retryOnFailedConnect"`
	ReconnectBufSize     int    `json:"reconnectBufSize"`
	PingInterval         int    `json:"pingInterval"`
	MaxPingsOut          int    `json:"maxPingsOut"`
}

// DeviceSubscription selects every telemetry device published by one gateway.
type DeviceSubscription struct {
	GatewayID string `json:"gatewayId"`
}

func Default() App {
	return App{
		Software: Software{ID: "thingsmodel-001", Name: "ThingsModel"},
		Logger:   logging.DefaultConfig().Logger,
		Subscriber: Subscriber{
			Enabled: false, URL: "nats://127.0.0.1:4222", Name: "thingsmodel-sub",
			InputSubjectPrefix: "powerpulse.gateway",
			ConnectTimeout:     2000, ReconnectWait: 2000, MaxReconnects: -1,
			RetryOnFailedConnect: true, PingInterval: 20000, MaxPingsOut: 3,
		},
		Publisher: Publisher{
			Enabled: false, URL: "nats://127.0.0.1:4222", Name: "thingsmodel-pub",
			QueueSize: 4096, ConnectTimeout: 2000, ReconnectWait: 2000, MaxReconnects: -1,
			RetryOnFailedConnect: true, ReconnectBufSize: 8388608, PingInterval: 20000, MaxPingsOut: 3,
		},
		StaleAfter:          60000,
		DeviceSubscriptions: []DeviceSubscription{},
	}
}

func Validate(app App) error {
	if !validSubjectToken(app.Software.ID) {
		return fmt.Errorf("software.id 不能为空，且只能包含字母、数字、下划线和连字符")
	}
	if strings.TrimSpace(app.Software.Name) == "" {
		return fmt.Errorf("software.name 不能为空")
	}
	if err := (logging.Config{Logger: app.Logger}).Validate(); err != nil {
		return err
	}
	subscriber := app.Subscriber
	if subscriber.Enabled && (strings.TrimSpace(subscriber.URL) == "" || strings.TrimSpace(subscriber.InputSubjectPrefix) == "") {
		return fmt.Errorf("启用数据订阅时必须填写服务地址和输入主题前缀")
	}
	if subscriber.ConnectTimeout < 0 || subscriber.ReconnectWait < 0 || subscriber.PingInterval < 0 || subscriber.MaxPingsOut < 0 || subscriber.MaxReconnects < -1 {
		return fmt.Errorf("数据订阅数值配置无效")
	}
	publisher := app.Publisher
	if publisher.Enabled && strings.TrimSpace(publisher.URL) == "" {
		return fmt.Errorf("启用内容发布时必须填写服务地址")
	}
	if publisher.QueueSize < 1 || publisher.ConnectTimeout < 0 || publisher.ReconnectWait < 0 || publisher.ReconnectBufSize < 0 || publisher.PingInterval < 0 || publisher.MaxPingsOut < 0 || publisher.MaxReconnects < -1 {
		return fmt.Errorf("内容发布数值配置无效")
	}
	if app.StaleAfter < 0 {
		return fmt.Errorf("数据过期时长不能小于 0")
	}
	seen := make(map[string]struct{}, len(app.DeviceSubscriptions))
	for _, subscription := range app.DeviceSubscriptions {
		if !validSubjectToken(subscription.GatewayID) {
			return fmt.Errorf("设备订阅的 gatewayId 不能为空，且只能包含字母、数字、下划线和连字符")
		}
		if _, duplicate := seen[subscription.GatewayID]; duplicate {
			return fmt.Errorf("设备订阅 gatewayId 重复: %s", subscription.GatewayID)
		}
		seen[subscription.GatewayID] = struct{}{}
	}
	return nil
}

func validSubjectToken(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-') {
			return false
		}
	}
	return true
}
