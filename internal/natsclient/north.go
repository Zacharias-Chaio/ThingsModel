// north.go - 北向客户端：面向下游模块。发布归一化数据到 powerpulse.thingsmodel.{id}.data，
// 并预留北向控制入口：未来其他模块下发控制指令后，经 CommandDispatcher 进程内直通南向客户端。
package natsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"thingsmodel/internal/config"

	"github.com/nats-io/nats.go"
)

// outputSubjectPrefix 是静态编码的输出主题前缀：powerpulse + 模块名称。
// 输出 subject 固定为 {prefix}.{softwareId}.{type}，不对外暴露配置。
const outputSubjectPrefix = "powerpulse.thingsmodel"

// 消息类型路由：data 为归一化设备状态，alarm 为预留的告警通道。
const (
	messageTypeData  = MessageTypeData
	messageTypeAlarm = "alarm"
)

// CommandDispatcher 把控制指令直通转发给南向客户端（进程内方法调用，不经 NATS 主题）。
// 由 SouthClient 实现；北向客户端只依赖此接口，不感知南向实现细节。
type CommandDispatcher interface {
	DispatchCommand(ctx context.Context, gatewayID string, cmd MessageCmd) (MessageAck, error)
}

// outbound 是发布队列中的待发消息：按类型路由到对应输出主题。
type outbound struct {
	messageType string
	payload     []byte
}

// NorthClient 是北向 NATS 客户端，与南向客户端互不影响。
type NorthClient struct {
	conn         *nats.Conn
	softwareID   string
	dataSubject  string
	alarmSubject string
	log          *slog.Logger
	events       chan outbound
	eventsMu     sync.Mutex
	eventsDone   bool
	stopOnce     sync.Once
	done         chan struct{}

	dispatcherMu sync.RWMutex
	dispatcher   CommandDispatcher
}

// NewNorthClient 按配置创建北向客户端；未启用时返回 (nil, nil)。
func NewNorthClient(ctx context.Context, app config.App) (*NorthClient, error) {
	northConfig := app.Publisher
	if !northConfig.Enabled {
		return nil, nil
	}
	if err := config.Validate(app); err != nil {
		return nil, err
	}
	log := slog.Default().With("module", "north")
	client := &NorthClient{
		softwareID:   app.Software.ID,
		dataSubject:  outputSubjectPrefix + "." + app.Software.ID + "." + messageTypeData,
		alarmSubject: outputSubjectPrefix + "." + app.Software.ID + "." + messageTypeAlarm,
		log:          log,
		events:       make(chan outbound, northConfig.QueueSize),
		done:         make(chan struct{}),
	}
	options := []nats.Option{
		nats.Name(northConfig.Name),
		nats.Timeout(milliseconds(northConfig.ConnectTimeout, 2*time.Second)),
		nats.ReconnectWait(milliseconds(northConfig.ReconnectWait, 2*time.Second)),
		nats.MaxReconnects(northConfig.MaxReconnects),
		nats.RetryOnFailedConnect(northConfig.RetryOnFailedConnect),
		nats.ReconnectBufSize(northConfig.ReconnectBufSize),
		nats.PingInterval(milliseconds(northConfig.PingInterval, 20*time.Second)),
		nats.MaxPingsOutstanding(northConfig.MaxPingsOut),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) { log.Warn("北向连接已断开", "error", err) }),
		nats.ReconnectHandler(func(connection *nats.Conn) { log.Info("北向连接已重连", "url", connection.ConnectedUrl()) }),
		nats.ClosedHandler(func(connection *nats.Conn) { log.Warn("北向连接已关闭", "error", connection.LastError()) }),
	}
	connection, err := nats.Connect(northConfig.URL, options...)
	if err != nil {
		return nil, fmt.Errorf("连接北向 NATS: %w", err)
	}
	client.conn = connection
	go client.publishLoop(ctx)
	log.Info("北向客户端已启动", "url", northConfig.URL, "data", client.dataSubject, "alarm", client.alarmSubject)
	return client, nil
}

// SetDispatcher 注入控制指令直通通道（南向客户端）。热重载后由 Manager 重新接线。
func (p *NorthClient) SetDispatcher(dispatcher CommandDispatcher) {
	p.dispatcherMu.Lock()
	defer p.dispatcherMu.Unlock()
	p.dispatcher = dispatcher
}

// SendCommand 把一条控制指令经进程内直通转发到南向客户端，再由其发往网关。
// 本期为预留入口：北向控制主题/HTTP 入口接入后调用此方法即可。
func (p *NorthClient) SendCommand(ctx context.Context, gatewayID string, cmd MessageCmd) (MessageAck, error) {
	p.dispatcherMu.RLock()
	dispatcher := p.dispatcher
	p.dispatcherMu.RUnlock()
	if dispatcher == nil {
		return MessageAck{}, fmt.Errorf("控制指令通道未接入：南向客户端不可用或未启用")
	}
	return dispatcher.DispatchCommand(ctx, gatewayID, cmd)
}

// PublishData 排队发布一条归一化设备状态到 .data 主题。
func (p *NorthClient) PublishData(data NormalizedData) {
	data.ThingsModelID = p.softwareID
	payload, err := json.Marshal(data)
	if err != nil {
		p.log.Warn("编码标准化遥测失败", "error", err)
		return
	}
	p.enqueue(outbound{messageType: messageTypeData, payload: payload})
}

// PublishAlarm 排队发布一条告警消息到 .alarm 主题。
// 告警业务逻辑尚未接入，当前仅提供主题路由与发布通道。
func (p *NorthClient) PublishAlarm(payload []byte) {
	p.enqueue(outbound{messageType: messageTypeAlarm, payload: payload})
}

// enqueue 入队且不阻塞调用方；队列满时丢弃最旧消息，优先保留最新状态。
func (p *NorthClient) enqueue(message outbound) {
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if p.eventsDone {
		return
	}
	select {
	case p.events <- message:
		return
	default:
	}
	select {
	case <-p.events:
		p.log.Warn("北向连接队列已满，丢弃最旧消息")
	default:
	}
	p.events <- message
}

func (p *NorthClient) Close() {
	p.stopOnce.Do(func() {
		p.eventsMu.Lock()
		p.eventsDone = true
		close(p.events)
		p.eventsMu.Unlock()
		<-p.done
		if err := p.conn.Drain(); err != nil {
			p.log.Warn("北向连接 Drain 失败", "error", err)
			p.conn.Close()
		}
	})
}

func (p *NorthClient) publishLoop(ctx context.Context) {
	defer close(p.done)
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-p.events:
			if !ok {
				return
			}
			subject := p.dataSubject
			if message.messageType == messageTypeAlarm {
				subject = p.alarmSubject
			}
			if err := p.conn.PublishMsg(NewEnvelope(message.messageType, message.payload).toMsg(subject)); err != nil {
				p.log.Warn("发布消息失败", "type", message.messageType, "error", err)
			}
		}
	}
}

func milliseconds(value int, fallback time.Duration) time.Duration {
	if value == 0 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}
