// north.go - 北向客户端：数据/告警发布（双队列隔离）+ 查询/控制应答挂载。
// 数据队列队满丢最旧（快照幂等，保最新）；告警队列绝不丢弃（沿消息不可再生）。
package natsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"thingsmodel/internal/config"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// outbound 是发布队列中的待发消息：主题 + 信封类型 + 负载。
type outbound struct {
	subject     string
	messageType string
	payload     []byte
}

// NorthClient 是北向 NATS 客户端，与南向客户端互不影响。
type NorthClient struct {
	conn         *nats.Conn
	softwareID   string
	source       AlarmSource
	dataSubject  string
	alarmSubject string
	log          *slog.Logger
	dataQueue    chan outbound
	alarmQueue   chan outbound
	eventsMu     sync.Mutex
	eventsDone   bool
	shutdown     chan struct{} // 关闭信号：先于关队列发出，解除 enqueueAlarm 的阻塞等待
	stopOnce     sync.Once
	done         chan struct{}
	responder    *NorthResponder
}

// NewNorthClient 按配置创建北向客户端；未启用时返回 (nil, nil)。
// service 非 nil 时同时订阅四个查询主题与 .cmd 控制主题（REQ/REP 应答）。
func NewNorthClient(ctx context.Context, app config.App, service NorthService) (*NorthClient, error) {
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
		source:       AlarmSource{Module: "thingsmodel", Instance: app.Software.ID},
		dataSubject:  SubjectData,
		alarmSubject: SubjectAlarm,
		log:          log,
		dataQueue:    make(chan outbound, northConfig.QueueSize),
		alarmQueue:   make(chan outbound, northConfig.QueueSize),
		shutdown:     make(chan struct{}),
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
	if service != nil {
		responder, err := NewNorthResponder(connection, service)
		if err != nil {
			connection.Close()
			return nil, fmt.Errorf("订阅北向服务主题失败: %w", err)
		}
		client.responder = responder
	}
	go client.publishLoop(ctx)
	log.Info("北向客户端已启动", "url", northConfig.URL, "data", client.dataSubject, "alarm", client.alarmSubject)
	return client, nil
}

// PublishData 排队发布一条归一化设备状态到 .data 主题（队满丢最旧，保最新快照）。
func (p *NorthClient) PublishData(data NormalizedData) {
	data.ThingsModelID = p.softwareID
	payload, err := json.Marshal(data)
	if err != nil {
		p.log.Warn("编码标准化遥测失败", "error", err)
		return
	}
	p.enqueueData(outbound{subject: p.dataSubject, messageType: MessageTypeData, payload: payload})
}

// PublishAlarm 排队发布一条标准化告警/事件消息（不丢弃策略：沿消息不可再生）。
// AlarmID/Source/Timestamp 缺省时由本方法补齐（uuid、本模块来源、当前时间）。
func (p *NorthClient) PublishAlarm(message AlarmMessage) {
	if message.AlarmID == "" {
		message.AlarmID = uuid.NewString()
	}
	if message.Source == (AlarmSource{}) {
		message.Source = p.source
	}
	if message.Timestamp == 0 {
		message.Timestamp = time.Now().UTC().UnixMilli()
	}
	payload, err := json.Marshal(message)
	if err != nil {
		p.log.Warn("编码告警消息失败", "error", err)
		return
	}
	p.enqueueAlarm(outbound{subject: p.alarmSubject, messageType: MessageTypeAlarm, payload: payload})
}

// enqueueData 入队数据消息且不阻塞调用方；队列满时丢弃最旧消息，优先保留最新状态。
func (p *NorthClient) enqueueData(message outbound) {
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if p.eventsDone {
		return
	}
	select {
	case p.dataQueue <- message:
		return
	default:
	}
	select {
	case <-p.dataQueue:
		p.log.Warn("北向数据队列已满，丢弃最旧消息")
	default:
	}
	p.dataQueue <- message
}

// enqueueAlarm 入队告警消息：队列满时阻塞等待空间，绝不丢弃（契约：沿消息/终态必达）。
// 等待期间若客户端开始关闭（shutdown 先于关队列发出），放弃入队直接返回，
// 避免 publishLoop 已退出时阻塞持有 eventsMu 导致 Close 死锁。
func (p *NorthClient) enqueueAlarm(message outbound) {
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if p.eventsDone {
		return
	}
	select {
	case p.alarmQueue <- message:
		return
	default:
	}
	p.log.Warn("北向告警队列已满，等待可用空间（不丢弃）")
	select {
	case p.alarmQueue <- message:
	case <-p.shutdown:
		p.log.Warn("北向客户端正在关闭，告警消息未入队")
	}
}

func (p *NorthClient) Close() {
	p.stopOnce.Do(func() {
		close(p.shutdown) // 先解除 enqueueAlarm 的阻塞等待，再获取锁关队列
		if p.responder != nil {
			p.responder.Close()
		}
		p.eventsMu.Lock()
		p.eventsDone = true
		close(p.dataQueue)
		close(p.alarmQueue)
		p.eventsMu.Unlock()
		<-p.done
		if err := p.conn.Drain(); err != nil {
			p.log.Warn("北向连接 Drain 失败", "error", err)
			p.conn.Close()
		}
	})
}

// publishLoop 依次发布两条队列中的消息；两条队列均关闭并排空后退出。
func (p *NorthClient) publishLoop(ctx context.Context) {
	defer close(p.done)
	dataQueue, alarmQueue := p.dataQueue, p.alarmQueue
	for dataQueue != nil || alarmQueue != nil {
		var message outbound
		var ok bool
		select {
		case <-ctx.Done():
			return
		case message, ok = <-dataQueue:
			if !ok {
				dataQueue = nil
				continue
			}
		case message, ok = <-alarmQueue:
			if !ok {
				alarmQueue = nil
				continue
			}
		}
		if err := p.conn.PublishMsg(NewEnvelope(message.messageType, message.payload).toMsg(message.subject)); err != nil {
			p.log.Warn("发布消息失败", "type", message.messageType, "subject", message.subject, "error", err)
		}
	}
}

func milliseconds(value int, fallback time.Duration) time.Duration {
	if value == 0 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}
