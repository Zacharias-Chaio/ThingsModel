// northapi.go - 北向 REQ/REP 服务：四个查询主题 + .cmd 服务控制下发。
// 响应在订阅回调内直接 reply（经连接发送，不经发布队列），信封回填请求 PP-Message-ID。
package natsclient

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// NorthService 由 runtime 提供的北向服务实现：查询取数与控制调用。
type NorthService interface {
	Devices() DevicesQueryResponse
	Properties(deviceID string) PropertiesQueryResponse
	Methods(deviceID string) MethodsQueryResponse
	Events(deviceID string) EventsQueryResponse
	Control(ctx context.Context, request ControlRequest, requestID string) ControlAck
}

// NorthResponder 订阅北向服务主题并把请求分发给 NorthService。
type NorthResponder struct {
	conn          *nats.Conn
	service       NorthService
	log           *slog.Logger
	subscriptions []*nats.Subscription
}

// NewNorthResponder 订阅四个查询主题与控制主题；订阅失败时回滚已建立的订阅。
func NewNorthResponder(conn *nats.Conn, service NorthService) (*NorthResponder, error) {
	responder := &NorthResponder{conn: conn, service: service, log: slog.Default().With("module", "northapi")}
	routes := []struct {
		subject string
		handler func(*nats.Msg)
	}{
		{SubjectQueryDevices, responder.handleDevices},
		{SubjectQueryProperties, responder.handleProperties},
		{SubjectQueryMethods, responder.handleMethods},
		{SubjectQueryEvents, responder.handleEvents},
		{SubjectControl, responder.handleControl},
	}
	for _, route := range routes {
		subscription, err := conn.Subscribe(route.subject, route.handler)
		if err != nil {
			responder.Close()
			return nil, err
		}
		responder.subscriptions = append(responder.subscriptions, subscription)
	}
	if err := conn.Flush(); err != nil {
		responder.Close()
		return nil, err
	}
	responder.log.Info("北向服务主题已订阅", "subjects", len(routes))
	return responder, nil
}

// Close 取消全部服务主题订阅。
func (r *NorthResponder) Close() {
	for _, subscription := range r.subscriptions {
		if err := subscription.Unsubscribe(); err != nil {
			r.log.Warn("取消北向服务主题订阅失败", "subject", subscription.Subject, "error", err)
		}
	}
	r.subscriptions = nil
}

// request 解析请求信封；信封无效时只记日志（无法构造有效应答）。
func (r *NorthResponder) request(msg *nats.Msg) (Envelope, bool) {
	envelope, err := envelopeFromMsg(msg)
	if err != nil {
		r.log.Warn("北向请求信封无效", "subject", msg.Subject, "error", err)
		return Envelope{}, false
	}
	return envelope, true
}

// reply 把应答负载发布到请求的 Reply 主题，信封 ID 回填请求 PP-Message-ID。
func (r *NorthResponder) reply(msg *nats.Msg, request Envelope, payload any) {
	if msg.Reply == "" {
		r.log.Warn("北向请求缺少 Reply 主题，丢弃应答", "subject", msg.Subject, "request_id", request.ID)
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		r.log.Warn("编码北向应答失败", "subject", msg.Subject, "error", err)
		return
	}
	envelope := Envelope{ID: request.ID, Type: request.Type, Version: MessageVersion, Timestamp: time.Now().UTC(), Payload: data}
	if err := r.conn.PublishMsg(envelope.toMsg(msg.Reply)); err != nil {
		r.log.Warn("发布北向应答失败", "subject", msg.Subject, "error", err)
	}
}

func (r *NorthResponder) handleDevices(msg *nats.Msg) {
	envelope, valid := r.request(msg)
	if !valid {
		return
	}
	r.reply(msg, envelope, r.service.Devices())
}

func (r *NorthResponder) handleProperties(msg *nats.Msg) {
	envelope, valid := r.request(msg)
	if !valid {
		return
	}
	var request PropertiesQueryRequest
	if len(envelope.Payload) > 0 {
		if err := json.Unmarshal(envelope.Payload, &request); err != nil {
			r.reply(msg, envelope, PropertiesQueryResponse{OK: false, Message: "请求解析失败: " + err.Error(), DeviceID: request.DeviceID})
			return
		}
	}
	r.reply(msg, envelope, r.service.Properties(request.DeviceID))
}

func (r *NorthResponder) handleMethods(msg *nats.Msg) {
	envelope, valid := r.request(msg)
	if !valid {
		return
	}
	var request MethodsQueryRequest
	if len(envelope.Payload) > 0 {
		if err := json.Unmarshal(envelope.Payload, &request); err != nil {
			r.reply(msg, envelope, MethodsQueryResponse{OK: false, Message: "请求解析失败: " + err.Error(), DeviceID: request.DeviceID})
			return
		}
	}
	r.reply(msg, envelope, r.service.Methods(request.DeviceID))
}

func (r *NorthResponder) handleEvents(msg *nats.Msg) {
	envelope, valid := r.request(msg)
	if !valid {
		return
	}
	var request EventsQueryRequest
	if len(envelope.Payload) > 0 {
		if err := json.Unmarshal(envelope.Payload, &request); err != nil {
			r.reply(msg, envelope, EventsQueryResponse{OK: false, Message: "请求解析失败: " + err.Error(), DeviceID: request.DeviceID})
			return
		}
	}
	r.reply(msg, envelope, r.service.Events(request.DeviceID))
}

func (r *NorthResponder) handleControl(msg *nats.Msg) {
	envelope, valid := r.request(msg)
	if !valid {
		return
	}
	var request ControlRequest
	if len(envelope.Payload) > 0 {
		if err := json.Unmarshal(envelope.Payload, &request); err != nil {
			r.reply(msg, envelope, ControlAck{Status: AckStatusFailure, Message: "请求解析失败: " + err.Error()})
			return
		}
	}
	ack := r.service.Control(context.Background(), request, envelope.ID)
	if ack.RequestID == "" {
		ack.RequestID = envelope.ID
	}
	r.reply(msg, envelope, ack)
}
