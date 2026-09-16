package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"thingsmodel/internal/config"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Setting 是平台配置的单行持久化表，ConfigJSON 保存完整的 config.App 快照。
type Setting struct {
	ID          int       `gorm:"primaryKey" json:"-"`
	ConfigJSON  []byte    `gorm:"not null" json:"-"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

const settingsRowID = 1

// SettingsStore 以数据库为唯一配置源的平台设置存储。
// 启动时 OpenSettings 创建表结构并加载已有配置；不存在时写入默认配置。
type SettingsStore struct {
	db  *gorm.DB
	mu  sync.RWMutex
	app config.App
}

// OpenSettings 加载或初始化平台配置。首次启动（表中无记录）时持久化默认配置；
// 已有记录缺失的字段回落到默认值，保证向后兼容。
func OpenSettings(db *gorm.DB) (*SettingsStore, error) {
	var record Setting
	// 首次启动查不到记录属预期路径，丢弃 ORM 日志避免误报 record not found 错误
	err := db.Session(&gorm.Session{Logger: logger.Discard}).First(&record, "id = ?", settingsRowID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		app := config.Default()
		raw, err := json.Marshal(app)
		if err != nil {
			return nil, fmt.Errorf("序列化默认配置失败: %w", err)
		}
		record = Setting{ID: settingsRowID, ConfigJSON: raw, UpdatedAt: time.Now().UTC()}
		if err := db.Create(&record).Error; err != nil {
			return nil, fmt.Errorf("写入默认配置失败: %w", err)
		}
		return &SettingsStore{db: db, app: app}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取平台配置失败: %w", err)
	}
	app, migrated, err := decodeSettings(record.ConfigJSON)
	if err != nil {
		return nil, fmt.Errorf("解析平台配置失败: %w", err)
	}
	if migrated {
		// 旧版单一 nats 配置段拆分为 subscriber/publisher 后立即写回数据库
		raw, err := json.Marshal(app)
		if err != nil {
			return nil, fmt.Errorf("序列化迁移后的平台配置失败: %w", err)
		}
		if err := db.Save(&Setting{ID: settingsRowID, ConfigJSON: raw, UpdatedAt: time.Now().UTC()}).Error; err != nil {
			return nil, fmt.Errorf("写回迁移后的平台配置失败: %w", err)
		}
	}
	return &SettingsStore{db: db, app: app}, nil
}

// Get 返回当前配置的深拷贝。
func (s *SettingsStore) Get() config.App {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneApp(s.app)
}

// Save 校验并持久化配置到数据库，同时更新内存副本。
func (s *SettingsStore) Save(app config.App) error {
	if err := config.Validate(app); err != nil {
		return err
	}
	raw, err := json.Marshal(app)
	if err != nil {
		return fmt.Errorf("序列化平台配置失败: %w", err)
	}
	record := Setting{ID: settingsRowID, ConfigJSON: raw, UpdatedAt: time.Now().UTC()}
	if err := s.db.Save(&record).Error; err != nil {
		return fmt.Errorf("写入平台配置失败: %w", err)
	}
	s.mu.Lock()
	s.app = cloneApp(app)
	s.mu.Unlock()
	return nil
}

// decodeSettings 在默认值基础上反序列化，缺失字段保持默认。
// 检测到旧版单一 nats 配置段时，将其参数拆分映射到 subscriber/publisher
// 并返回 migrated=true，由调用方写回数据库完成一次性迁移。
func decodeSettings(raw []byte) (app config.App, migrated bool, err error) {
	app = config.Default()
	if err := json.Unmarshal(raw, &app); err != nil {
		return config.App{}, false, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return config.App{}, false, err
	}
	legacyRaw, hasLegacy := probe["nats"]
	if !hasLegacy {
		return app, false, nil
	}
	var legacy struct {
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
		InputSubjectPrefix   string `json:"inputSubjectPrefix"`
		StaleAfter           int    `json:"staleAfter"`
	}
	if err := json.Unmarshal(legacyRaw, &legacy); err != nil {
		return config.App{}, false, err
	}
	app.Subscriber = config.Subscriber{
		Enabled: legacy.Enabled, URL: legacy.URL, Name: legacy.Name + "-sub",
		InputSubjectPrefix: legacy.InputSubjectPrefix,
		ConnectTimeout:     legacy.ConnectTimeout, ReconnectWait: legacy.ReconnectWait,
		MaxReconnects: legacy.MaxReconnects, RetryOnFailedConnect: legacy.RetryOnFailedConnect,
		PingInterval: legacy.PingInterval, MaxPingsOut: legacy.MaxPingsOut,
	}
	app.Publisher = config.Publisher{
		Enabled: legacy.Enabled, URL: legacy.URL, Name: legacy.Name + "-pub",
		QueueSize: legacy.QueueSize, ConnectTimeout: legacy.ConnectTimeout,
		ReconnectWait: legacy.ReconnectWait, MaxReconnects: legacy.MaxReconnects,
		RetryOnFailedConnect: legacy.RetryOnFailedConnect, ReconnectBufSize: legacy.ReconnectBufSize,
		PingInterval: legacy.PingInterval, MaxPingsOut: legacy.MaxPingsOut,
	}
	if legacy.StaleAfter > 0 {
		app.StaleAfter = legacy.StaleAfter
	}
	return app, true, nil
}

func cloneApp(app config.App) config.App {
	app.DeviceSubscriptions = append([]config.DeviceSubscription(nil), app.DeviceSubscriptions...)
	return app
}
