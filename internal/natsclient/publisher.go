// publisher.go - 内容发布客户端：把归一化数据与业务消息发布给下游。
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

// 消息类型路由：data 为本期的归一化设备状态，alarm 为预留的告警通道。
const (
	messageTypeData  = "data"
	messageTypeAlarm = "alarm"
)

// outbound 是发布队列中的待发消息：按类型路由到对应输出主题。
type outbound struct {
	messageType string
	payload     []byte
}

// Publisher 是独立的 NATS 内容发布客户端，与订阅客户端互不影响。
type Publisher struct {
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
}

func NewPublisher(ctx context.Context, app config.App) (*Publisher, error) {
	publisherConfig := app.Publisher
	if !publisherConfig.Enabled {
		return nil, nil
	}
	if err := config.Validate(app); err != nil {
		return nil, err
	}
	log := slog.Default().With("module", "publisher")
	publisher := &Publisher{
		softwareID:   app.Software.ID,
		dataSubject:  outputSubjectPrefix + "." + app.Software.ID + "." + messageTypeData,
		alarmSubject: outputSubjectPrefix + "." + app.Software.ID + "." + messageTypeAlarm,
		log:          log,
		events:       make(chan outbound, publisherConfig.QueueSize),
		done:         make(chan struct{}),
	}
	options := []nats.Option{
		nats.Name(publisherConfig.Name),
		nats.Timeout(milliseconds(publisherConfig.ConnectTimeout, 2*time.Second)),
		nats.ReconnectWait(milliseconds(publisherConfig.ReconnectWait, 2*time.Second)),
		nats.MaxReconnects(publisherConfig.MaxReconnects),
		nats.RetryOnFailedConnect(publisherConfig.RetryOnFailedConnect),
		nats.ReconnectBufSize(publisherConfig.ReconnectBufSize),
		nats.PingInterval(milliseconds(publisherConfig.PingInterval, 20*time.Second)),
		nats.MaxPingsOutstanding(publisherConfig.MaxPingsOut),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) { log.Warn("内容发布连接已断开", "error", err) }),
		nats.ReconnectHandler(func(connection *nats.Conn) { log.Info("内容发布连接已重连", "url", connection.ConnectedUrl()) }),
		nats.ClosedHandler(func(connection *nats.Conn) { log.Warn("内容发布连接已关闭", "error", connection.LastError()) }),
	}
	connection, err := nats.Connect(publisherConfig.URL, options...)
	if err != nil {
		return nil, fmt.Errorf("连接内容发布 NATS: %w", err)
	}
	publisher.conn = connection
	go publisher.publishLoop(ctx)
	log.Info("内容发布客户端已启动", "url", publisherConfig.URL, "data", publisher.dataSubject, "alarm", publisher.alarmSubject)
	return publisher, nil
}

// PublishData 排队发布一条归一化设备状态到 .data 主题。
func (p *Publisher) PublishData(data NormalizedData) {
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
func (p *Publisher) PublishAlarm(payload []byte) {
	p.enqueue(outbound{messageType: messageTypeAlarm, payload: payload})
}

// enqueue 入队且不阻塞调用方；队列满时丢弃最旧消息，优先保留最新状态。
func (p *Publisher) enqueue(message outbound) {
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
		p.log.Warn("内容发布队列已满，丢弃最旧消息")
	default:
	}
	p.events <- message
}

func (p *Publisher) Close() {
	p.stopOnce.Do(func() {
		p.eventsMu.Lock()
		p.eventsDone = true
		close(p.events)
		p.eventsMu.Unlock()
		<-p.done
		if err := p.conn.Drain(); err != nil {
			p.log.Warn("内容发布 Drain 失败", "error", err)
			p.conn.Close()
		}
	})
}

func (p *Publisher) publishLoop(ctx context.Context) {
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
			if err := p.conn.PublishMsg(newEnvelope(message.messageType, message.payload).toMsg(subject)); err != nil {
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
