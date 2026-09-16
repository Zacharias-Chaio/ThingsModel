// subscriber.go - 数据订阅客户端：只负责消费网关遥测。
package natsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"thingsmodel/internal/config"

	"github.com/nats-io/nats.go"
)

// TelemetryHandler 接收一条已通过信封校验的网关遥测。
// 归一化、处理管道与发布由 Manager 组装，订阅客户端不做任何业务逻辑。
type TelemetryHandler func(MessageData, time.Time)

// Subscriber 是独立的 NATS 数据订阅客户端，与发布客户端互不影响。
type Subscriber struct {
	conn    *nats.Conn
	handler TelemetryHandler
	log     *slog.Logger
}

func NewSubscriber(ctx context.Context, app config.App, handler TelemetryHandler) (*Subscriber, error) {
	subscriberConfig := app.Subscriber
	if !subscriberConfig.Enabled {
		return nil, nil
	}
	if err := config.Validate(app); err != nil {
		return nil, err
	}
	log := slog.Default().With("module", "subscriber")
	subscriber := &Subscriber{handler: handler, log: log}
	options := []nats.Option{
		nats.Name(subscriberConfig.Name),
		nats.Timeout(milliseconds(subscriberConfig.ConnectTimeout, 2*time.Second)),
		nats.ReconnectWait(milliseconds(subscriberConfig.ReconnectWait, 2*time.Second)),
		nats.MaxReconnects(subscriberConfig.MaxReconnects),
		nats.RetryOnFailedConnect(subscriberConfig.RetryOnFailedConnect),
		nats.PingInterval(milliseconds(subscriberConfig.PingInterval, 20*time.Second)),
		nats.MaxPingsOutstanding(subscriberConfig.MaxPingsOut),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) { log.Warn("数据订阅连接已断开", "error", err) }),
		nats.ReconnectHandler(func(connection *nats.Conn) { log.Info("数据订阅连接已重连", "url", connection.ConnectedUrl()) }),
		nats.ClosedHandler(func(connection *nats.Conn) { log.Warn("数据订阅连接已关闭", "error", connection.LastError()) }),
	}
	connection, err := nats.Connect(subscriberConfig.URL, options...)
	if err != nil {
		return nil, fmt.Errorf("连接数据订阅 NATS: %w", err)
	}
	subscriber.conn = connection
	prefix := strings.TrimSuffix(subscriberConfig.InputSubjectPrefix, ".")
	for _, subscription := range app.DeviceSubscriptions {
		gatewayID := subscription.GatewayID
		subject := prefix + "." + gatewayID + ".data"
		if _, err := connection.Subscribe(subject, func(message *nats.Msg) { subscriber.handleTelemetry(gatewayID, message) }); err != nil {
			connection.Close()
			return nil, fmt.Errorf("订阅数据主题 %s: %w", subject, err)
		}
	}
	if err := connection.Flush(); err != nil {
		connection.Close()
		return nil, fmt.Errorf("初始化数据订阅: %w", err)
	}
	log.Info("数据订阅客户端已启动", "url", subscriberConfig.URL, "subscriptions", len(app.DeviceSubscriptions))
	return subscriber, nil
}

func (s *Subscriber) handleTelemetry(expectedGatewayID string, message *nats.Msg) {
	envelope, err := envelopeFromMsg(message)
	if err != nil || envelope.Type != "data" || envelope.Version != messageVersion {
		s.log.Warn("忽略无效遥测消息", "error", err)
		return
	}
	var data MessageData
	if err := json.Unmarshal(envelope.Payload, &data); err != nil {
		s.log.Warn("解析网关遥测失败", "error", err)
		return
	}
	if data.GatewayID != expectedGatewayID {
		s.log.Warn("遥测网关 ID 与订阅不符", "expected", expectedGatewayID, "actual", data.GatewayID)
		return
	}
	if s.handler == nil {
		return
	}
	s.handler(data, envelope.Timestamp.UTC())
}

func (s *Subscriber) Close() {
	if s.conn == nil {
		return
	}
	if err := s.conn.Drain(); err != nil {
		s.log.Warn("数据订阅 Drain 失败", "error", err)
		s.conn.Close()
	}
}
