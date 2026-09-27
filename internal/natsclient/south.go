// south.go - 南向客户端：面向网关侧。订阅 {prefix}.{gw}.data 遥测与 cmdAck 回报，
// 发起 {prefix}.{gw}.cmd 控制下发（REQ/REP）与 {prefix}.{gw}.query 拓扑查询（REQ/REP）。
package natsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"thingsmodel/internal/config"

	"github.com/nats-io/nats.go"
)

// 南向交互默认参数。
const (
	defaultRequestTimeout = 5 * time.Second  // REQ/REP 同步请求超时
	defaultCmdAckTimeout  = 30 * time.Second // cmdAck 异步回报等待上限
)

// TelemetryHandler 接收一条已通过信封校验的网关遥测。
// 归一化、处理管道与发布由 Manager 组装，南向客户端不做任何业务逻辑。
type TelemetryHandler func(MessageData, time.Time)

// CmdAckHandler 接收一条控制命令的异步执行结果（网关复用 .data 主题回报）。
type CmdAckHandler func(MessageAck)

// SouthClient 是南向 NATS 客户端，与北向客户端互不影响。
// 除消费遥测外，还承担控制下发与拓扑查询的请求方角色。
type SouthClient struct {
	conn    *nats.Conn
	handler TelemetryHandler
	log     *slog.Logger

	prefix string

	stop      chan struct{}
	stopOnce  sync.Once
	pendingMu sync.Mutex
	pending   map[string]pendingAck // request_id → cmdAck 等待者
	onCmdAck  CmdAckHandler
}

// pendingAck 记录一个等待 cmdAck 的请求：等待通道与登记时间（用于超时清理）。
type pendingAck struct {
	waiter  chan MessageAck
	created time.Time
}

// NewSouthClient 按配置创建南向客户端；未启用时返回 (nil, nil)。
// ctx 仅为签名兼容保留：sweeper 生命周期跟随 Close，不再依赖进程级 ctx。
func NewSouthClient(_ context.Context, app config.App, handler TelemetryHandler) (*SouthClient, error) {
	southConfig := app.Subscriber
	if !southConfig.Enabled {
		return nil, nil
	}
	if err := config.Validate(app); err != nil {
		return nil, err
	}
	log := slog.Default().With("module", "south")
	client := &SouthClient{
		handler: handler, log: log,
		prefix:  strings.TrimSuffix(southConfig.InputSubjectPrefix, "."),
		stop:    make(chan struct{}),
		pending: make(map[string]pendingAck),
	}
	options := []nats.Option{
		nats.Name(southConfig.Name),
		nats.Timeout(milliseconds(southConfig.ConnectTimeout, 2*time.Second)),
		nats.ReconnectWait(milliseconds(southConfig.ReconnectWait, 2*time.Second)),
		nats.MaxReconnects(southConfig.MaxReconnects),
		nats.RetryOnFailedConnect(southConfig.RetryOnFailedConnect),
		nats.PingInterval(milliseconds(southConfig.PingInterval, 20*time.Second)),
		nats.MaxPingsOutstanding(southConfig.MaxPingsOut),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) { log.Warn("南向连接已断开", "error", err) }),
		nats.ReconnectHandler(func(connection *nats.Conn) { log.Info("南向连接已重连", "url", connection.ConnectedUrl()) }),
		nats.ClosedHandler(func(connection *nats.Conn) { log.Warn("南向连接已关闭", "error", connection.LastError()) }),
	}
	connection, err := nats.Connect(southConfig.URL, options...)
	if err != nil {
		return nil, fmt.Errorf("连接南向 NATS: %w", err)
	}
	client.conn = connection
	for _, subscription := range app.DeviceSubscriptions {
		gatewayID := subscription.GatewayID
		subject := client.dataSubject(gatewayID)
		if _, err := connection.Subscribe(subject, func(message *nats.Msg) { client.handleInbound(gatewayID, message) }); err != nil {
			connection.Close()
			return nil, fmt.Errorf("订阅数据主题 %s: %w", subject, err)
		}
	}
	if err := connection.Flush(); err != nil {
		connection.Close()
		return nil, fmt.Errorf("初始化南向连接: %w", err)
	}
	go client.sweepPending()
	log.Info("南向客户端已启动", "url", southConfig.URL, "subscriptions", len(app.DeviceSubscriptions))
	return client, nil
}

// OnCmdAck 注册控制命令异步执行结果的回调（进程内直通，Manager 负责接线）。
func (s *SouthClient) OnCmdAck(handler CmdAckHandler) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.onCmdAck = handler
}

// QueryTopology 向指定网关发起拓扑查询（REQ/REP），一次返回全量拓扑。
func (s *SouthClient) QueryTopology(ctx context.Context, gatewayID string) (*MessageQueryResp, error) {
	if s.conn == nil {
		return nil, fmt.Errorf("南向客户端未连接")
	}
	payload, err := json.Marshal(MessageQuery{Type: QueryTypeTopology})
	if err != nil {
		return nil, fmt.Errorf("编码拓扑查询请求: %w", err)
	}
	reply, err := s.request(ctx, s.querySubject(gatewayID), NewEnvelope(MessageTypeQuery, payload))
	if err != nil {
		return nil, err
	}
	var response MessageQueryResp
	if err := json.Unmarshal(reply.Payload, &response); err != nil {
		return nil, fmt.Errorf("解析拓扑查询响应: %w", err)
	}
	return &response, nil
}

// SendCommand 向指定网关下发单条写命令（REQ/REP）。
// 返回同步应答（accepted 表示已投递网关引擎写队列，failure 表示路由或参数错误），
// 并登记 pending 等待网关复用 .data 主题回报的 cmdAck 终态（success/failure）。
// final 为 nil 表示网关已直接拒绝，不会有异步终态。
func (s *SouthClient) SendCommand(ctx context.Context, gatewayID string, cmd MessageCmd) (ack MessageAck, final <-chan MessageAck, err error) {
	if s.conn == nil {
		return MessageAck{}, nil, fmt.Errorf("南向客户端未连接")
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		return MessageAck{}, nil, fmt.Errorf("编码写命令: %w", err)
	}
	envelope := NewEnvelope(MessageTypeCmd, payload)
	waiter := make(chan MessageAck, 1)
	s.pendingMu.Lock()
	s.pending[envelope.ID] = pendingAck{waiter: waiter, created: time.Now()}
	s.pendingMu.Unlock()
	reply, err := s.request(ctx, s.cmdSubject(gatewayID), envelope)
	if err != nil {
		s.removePending(envelope.ID)
		return MessageAck{}, nil, err
	}
	if err := json.Unmarshal(reply.Payload, &ack); err != nil {
		s.removePending(envelope.ID)
		return MessageAck{}, nil, fmt.Errorf("解析写命令应答: %w", err)
	}
	ack.RequestID = envelope.ID
	if ack.Status == AckStatusFailure {
		// 路由或参数错误，网关不会回报 cmdAck，直接撤销等待。
		s.removePending(envelope.ID)
		return ack, nil, nil
	}
	return ack, waiter, nil
}

// DispatchCommand 实现 CommandDispatcher：供北向客户端进程内直调，把控制指令发往网关。
func (s *SouthClient) DispatchCommand(ctx context.Context, gatewayID string, cmd MessageCmd) (MessageAck, error) {
	ack, _, err := s.SendCommand(ctx, gatewayID, cmd)
	return ack, err
}

func (s *SouthClient) request(ctx context.Context, subject string, envelope Envelope) (Envelope, error) {
	timeout := defaultRequestTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if wait := time.Until(deadline); wait > 0 && wait < timeout {
			timeout = wait
		}
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	message, err := s.conn.RequestMsgWithContext(requestCtx, envelope.toMsg(subject))
	if err != nil {
		return Envelope{}, fmt.Errorf("请求 %s: %w", subject, err)
	}
	return envelopeFromMsg(message)
}

// handleInbound 统一处理 {prefix}.{gw}.data 上的上行消息：
// type=data 为遥测，type=cmdAck 为控制命令异步执行结果。
func (s *SouthClient) handleInbound(expectedGatewayID string, message *nats.Msg) {
	envelope, err := envelopeFromMsg(message)
	if err != nil {
		s.log.Warn("忽略无效上行消息", "error", err)
		return
	}
	switch envelope.Type {
	case MessageTypeData:
		s.handleTelemetry(expectedGatewayID, envelope)
	case MessageTypeCmdAck:
		s.handleCmdAck(envelope)
	default:
		s.log.Warn("忽略未知消息类型", "type", envelope.Type)
	}
}

func (s *SouthClient) handleTelemetry(expectedGatewayID string, envelope Envelope) {
	if envelope.Version != MessageVersion {
		s.log.Warn("忽略不兼容的遥测版本", "version", envelope.Version)
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

// handleCmdAck 按请求 ID 匹配等待者并分发异步终态；未匹配时仅回调（如有注册）。
func (s *SouthClient) handleCmdAck(envelope Envelope) {
	var ack MessageAck
	if err := json.Unmarshal(envelope.Payload, &ack); err != nil {
		s.log.Warn("解析控制命令回报失败", "error", err)
		return
	}
	if ack.RequestID == "" {
		ack.RequestID = envelope.ID
	}
	s.pendingMu.Lock()
	entry, ok := s.pending[ack.RequestID]
	if ok {
		delete(s.pending, ack.RequestID)
	}
	handler := s.onCmdAck
	s.pendingMu.Unlock()
	if ok {
		entry.waiter <- ack
	}
	if handler != nil {
		handler(ack)
	}
}

// sweepPending 周期清理登记超过 defaultCmdAckTimeout 仍未收到 cmdAck 的等待者，
// 回报超时 failure 终态。随 Close 停止，避免热重载后泄漏。
func (s *SouthClient) sweepPending() {
	ticker := time.NewTicker(defaultCmdAckTimeout / 2)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-ticker.C:
			s.pendingMu.Lock()
			for id, entry := range s.pending {
				if now.Sub(entry.created) < defaultCmdAckTimeout {
					continue
				}
				select {
				case entry.waiter <- MessageAck{RequestID: id, Status: AckStatusFailure, Message: "等待网关执行回报超时"}:
					delete(s.pending, id)
				default:
					// 等待者尚未取走上一条回报（缓冲已满），本轮跳过。
				}
			}
			s.pendingMu.Unlock()
		}
	}
}

func (s *SouthClient) removePending(id string) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	delete(s.pending, id)
}

func (s *SouthClient) dataSubject(gatewayID string) string {
	return s.prefix + "." + gatewayID + ".data"
}
func (s *SouthClient) cmdSubject(gatewayID string) string { return s.prefix + "." + gatewayID + ".cmd" }
func (s *SouthClient) querySubject(gatewayID string) string {
	return s.prefix + "." + gatewayID + ".query"
}

func (s *SouthClient) Close() {
	if s.conn == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stop) })
	if err := s.conn.Drain(); err != nil {
		s.log.Warn("南向连接 Drain 失败", "error", err)
		s.conn.Close()
	}
}
