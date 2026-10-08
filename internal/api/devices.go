package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"thingsmodel/internal/store"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

func (s *Server) ListDevices(w http.ResponseWriter, r *http.Request) {
	configs, err := s.deviceConfigs()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, configs)
}

func (s *Server) GetDevice(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		fail(w, http.StatusServiceUnavailable, "设备数据库未就绪")
		return
	}
	id := chi.URLParam(r, "id")
	var device store.Device
	if err := s.DB.First(&device, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(w, http.StatusNotFound, "设备不存在")
			return
		}
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	config, err := device.Config()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, config)
}

func (s *Server) SaveDevice(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		fail(w, http.StatusServiceUnavailable, "设备数据库未就绪")
		return
	}
	var config store.DeviceConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		fail(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	normalizeDeviceConfig(&config)
	// 设备实例 ID 生成规则：Device-001、Device-002 ...（默认自动生成，不可修改）
	if config.ID == "" {
		id, err := nextDeviceID(s.DB)
		if err != nil {
			fail(w, http.StatusInternalServerError, "生成设备实例 ID 失败: "+err.Error())
			return
		}
		config.ID = id
	}
	var existing store.Device
	existingResult := s.DB.First(&existing, "id = ?", config.ID)
	if existingResult.Error != nil && !errors.Is(existingResult.Error, gorm.ErrRecordNotFound) {
		fail(w, http.StatusInternalServerError, existingResult.Error.Error())
		return
	}
	// 始终以模板最新定义刷新快照，仅保留实例已配置的 binding：
	// 模板新增的服务/告警会带入，删除的条目及其绑定会同步移除。
	template, err := s.templateForDevice(config.TemplateCode)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	hydrateDeviceSnapshot(&config, template)
	if err := s.validateDeviceConfig(&config); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	record, err := store.NewDeviceRecord(config)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if existingResult.Error == nil {
		record.CreatedAt = existing.CreatedAt
	}
	if err := s.DB.Save(&record).Error; err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.reloadRuntime(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, config)
}

func (s *Server) DeleteDevice(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		fail(w, http.StatusServiceUnavailable, "设备数据库未就绪")
		return
	}
	id := chi.URLParam(r, "id")
	result := s.DB.Delete(&store.Device{}, "id = ?", id)
	if result.Error != nil {
		fail(w, http.StatusInternalServerError, result.Error.Error())
		return
	}
	if result.RowsAffected == 0 {
		fail(w, http.StatusNotFound, "设备不存在")
		return
	}
	if s.Runtime != nil {
		s.Runtime.Remove(id)
	}
	ok(w, map[string]string{"id": id})
}

func (s *Server) ListRuntimeDevices(w http.ResponseWriter, r *http.Request) {
	if s.Runtime == nil {
		ok(w, []any{})
		return
	}
	ok(w, s.Runtime.Snapshot())
}

func (s *Server) reloadRuntime() error {
	if s.Runtime == nil {
		return nil
	}
	configs, err := s.deviceConfigs()
	if err != nil {
		return err
	}
	s.Runtime.Apply(configs)
	return nil
}

// ReloadRuntime loads all persisted device configurations into the active runtime.
func (s *Server) ReloadRuntime() error {
	return s.reloadRuntime()
}

func (s *Server) deviceConfigs() ([]store.DeviceConfig, error) {
	if s.DB == nil {
		return nil, errors.New("设备数据库未就绪")
	}
	var records []store.Device
	if err := s.DB.Order("id asc").Find(&records).Error; err != nil {
		return nil, err
	}
	configs := make([]store.DeviceConfig, 0, len(records))
	for _, record := range records {
		config, err := record.Config()
		if err != nil {
			return nil, fmt.Errorf("设备 %s: %w", record.ID, err)
		}
		configs = append(configs, config)
	}
	return configs, nil
}

// nextDeviceID 生成下一个设备实例 ID：扫描已有 Device-XXX 编号取最大值 +1，
// 格式为 Device-001、Device-002 ...；无历史设备时从 Device-001 开始。
func nextDeviceID(db *gorm.DB) (string, error) {
	var ids []string
	if err := db.Model(&store.Device{}).Pluck("id", &ids).Error; err != nil {
		return "", err
	}
	pattern := regexp.MustCompile(`^Device-(\d+)$`)
	max := 0
	for _, id := range ids {
		match := pattern.FindStringSubmatch(strings.TrimSpace(id))
		if match == nil {
			continue
		}
		if n, err := strconv.Atoi(match[1]); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("Device-%03d", max+1), nil
}

func (s *Server) validateDeviceConfig(config *store.DeviceConfig) error {
	if strings.TrimSpace(config.ID) == "" {
		return errors.New("设备 ID 不能为空")
	}
	if strings.TrimSpace(config.Name) == "" {
		return errors.New("设备名称不能为空")
	}
	if strings.TrimSpace(config.TemplateCode) == "" {
		return errors.New("请选择物模型模板")
	}
	if s.Templates == nil {
		return errors.New("模板服务未就绪")
	}
	template, err := s.templateForDevice(config.TemplateCode)
	if err != nil {
		return err
	}
	if config.TemplateVersion == "" {
		config.TemplateVersion = template.Version
	}
	for _, property := range config.Properties {
		if err := validatePropertyBinding(property); err != nil {
			return fmt.Errorf("属性 %s: %w", property.Key, err)
		}
	}
	for _, method := range config.Methods {
		if err := validateMethodBinding(method, config.Properties); err != nil {
			return fmt.Errorf("服务 %s: %w", method.Key, err)
		}
	}
	if err := validateAlarmEvents(config.Events, config.Properties); err != nil {
		return err
	}
	return nil
}

func (s *Server) templateForDevice(code string) (*store.Template, error) {
	if s.Templates == nil {
		return nil, errors.New("模板服务未就绪")
	}
	template, err := s.Templates.Get(code)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errors.New("引用的物模型模板不存在")
	}
	return template, err
}

// hydrateDeviceSnapshot copies a template's definitions while retaining only
// instance-specific bindings supplied by the client.
func hydrateDeviceSnapshot(config *store.DeviceConfig, template *store.Template) {
	propertyBindings := make(map[string]store.Binding, len(config.Properties))
	propertyModes := make(map[string]string, len(config.Properties))
	for _, property := range config.Properties {
		propertyBindings[property.Key] = property.Binding
		propertyModes[property.Key] = property.Mode
	}
	methodBindings := make(map[string]store.MethodBinding, len(config.Methods))
	for _, method := range config.Methods {
		methodBindings[method.Key] = method.Binding
	}
	eventBindings := make(map[string]store.EventBinding, len(config.Events))
	for _, event := range config.Events {
		if event.Binding.PropertyKey != "" {
			eventBindings[event.Key] = event.Binding
		}
	}

	config.TemplateVersion = template.Version
	config.Properties = make([]store.Property, len(template.Properties))
	for index, property := range template.Properties {
		property.Binding = propertyBindings[property.Key]
		if property.Binding.Sources == nil {
			property.Binding.Sources = []store.BindingSource{}
		}
		property.Mode = propertyModes[property.Key]
		if property.Mode != store.PropertyModeLogical {
			property.Mode = store.PropertyModePhysical
		}
		property.ID = fmt.Sprintf("property_%d", property.Number)
		config.Properties[index] = property
	}
	config.Methods = make([]store.Method, len(template.Methods))
	for index, method := range template.Methods {
		method.Binding = methodBindings[method.Key]
		method.ID = fmt.Sprintf("method_%d", method.Number)
		config.Methods[index] = method
	}
	// 告警：以模板最新定义为准，按 key 保留实例已配置的监测点位绑定；
	// 模板新增的告警带入（binding 留空），删除的告警及其绑定一并移除。
	config.Events = make(store.Events, len(template.Events))
	for index, event := range template.Events {
		event.Binding = eventBindings[event.Key]
		event.ID = fmt.Sprintf("event_%d", event.Number)
		config.Events[index] = event
	}
}

// syncDevicesFromTemplate 把模板的最新定义同步到所有引用该模板的设备实例：
// 重新水合快照（按 key 保留已配置绑定）、移除模板中已删除的告警条目并持久化。
// 返回成功更新的设备数量；单个设备失败时中止并返回错误。
func (s *Server) syncDevicesFromTemplate(template *store.Template) (int, error) {
	if s.DB == nil {
		return 0, errors.New("设备数据库未就绪")
	}
	var records []store.Device
	if err := s.DB.Where("template_code = ?", template.Code).Find(&records).Error; err != nil {
		return 0, err
	}
	updated := 0
	for _, record := range records {
		config, err := record.Config()
		if err != nil {
			return updated, fmt.Errorf("设备 %s: %w", record.ID, err)
		}
		hydrateDeviceSnapshot(&config, template)
		if err := s.validateDeviceConfig(&config); err != nil {
			return updated, fmt.Errorf("设备 %s: %w", record.ID, err)
		}
		nextRecord, err := store.NewDeviceRecord(config)
		if err != nil {
			return updated, err
		}
		nextRecord.CreatedAt = record.CreatedAt
		if err := s.DB.Save(&nextRecord).Error; err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

func normalizeDeviceConfig(config *store.DeviceConfig) {
	// 属性点位模式归一化：默认 physical；逻辑点位不保留来源。
	for i := range config.Properties {
		property := &config.Properties[i]
		if property.Mode != store.PropertyModeLogical {
			property.Mode = store.PropertyModePhysical
		} else {
			property.Binding.Sources = []store.BindingSource{}
		}
		property.Binding.Sources = stripEmptySources(property.Binding.Sources)
	}
	// 告警条目：点位绑定去空白
	for i := range config.Events {
		config.Events[i].Binding.PropertyKey = strings.TrimSpace(config.Events[i].Binding.PropertyKey)
	}
	for i := range config.Methods {
		if strings.TrimSpace(config.Methods[i].Binding.PropertyKey) == "" {
			config.Methods[i].Binding = store.MethodBinding{}
		}
	}
	config.ID = strings.TrimSpace(config.ID)
	config.Name = strings.TrimSpace(config.Name)
	config.TemplateCode = strings.TrimSpace(config.TemplateCode)
	config.Description = strings.TrimSpace(config.Description)
	if config.Properties == nil {
		config.Properties = []store.Property{}
	}
	if config.Methods == nil {
		config.Methods = []store.Method{}
	}
	if config.Events == nil {
		config.Events = store.Events{}
	}
}

func validatePropertyBinding(property store.Property) error {
	binding := property.Binding
	method := binding.Method
	if method == "" {
		method = "ept"
	}
	if property.Mode == store.PropertyModePhysical {
		// 物理点位：只支持 ept 直接绑定，且至多一个来源（可留空=未绑定）
		if method != "ept" {
			return errors.New("物理点位只支持 ept 直接绑定")
		}
		if len(binding.Sources) > 1 {
			return errors.New("物理点位只能绑定一个来源")
		}
	} else {
		// 逻辑点位
		if method == "ept" {
			// ept 逻辑点位：由服务下发或外部写入，不绑定来源
			if len(binding.Sources) > 0 {
				return errors.New("ept 逻辑点位由服务下发或外部写入，不应绑定来源")
			}
			return nil
		}
		// 非 ept 逻辑点位：多来源聚合，只读无写入权限
		if property.Type == "status" {
			return errors.New("状态属性只支持 ept 绑定")
		}
		switch method {
		case "sum", "avg", "min", "max", "and", "or":
			if len(binding.Sources) < 2 {
				return fmt.Errorf("%s 聚合至少需要两个物理来源", method)
			}
		default:
			return fmt.Errorf("不支持的聚合方法: %s", method)
		}
	}
	for _, source := range binding.Sources {
		if err := validateSource(source); err != nil {
			return err
		}
	}
	return nil
}

func validateMethodBinding(method store.Method, properties []store.Property) error {
	propertyKey := strings.TrimSpace(method.Binding.PropertyKey)
	if propertyKey == "" {
		return nil // 未绑定：留空表示暂不启用该服务
	}
	target, ok := findProperty(properties, propertyKey)
	if !ok {
		return fmt.Errorf("目标属性点位 %s 不存在", propertyKey)
	}
	if method.Type != target.Type {
		return fmt.Errorf("服务类型(%s)与目标属性类型(%s)不一致", method.Type, target.Type)
	}
	// 控制目标必须可写：物理点位，或 ept 逻辑点位；非 ept 逻辑点位为只读聚合点位。
	if target.Mode == store.PropertyModeLogical && target.Binding.Method != "" && target.Binding.Method != "ept" {
		return fmt.Errorf("目标属性 %s 为聚合逻辑点位，只读不可作为控制目标", target.Name)
	}
	return nil
}

func findProperty(properties []store.Property, key string) (store.Property, bool) {
	for _, property := range properties {
		if property.Key == key {
			return property, true
		}
	}
	return store.Property{}, false
}

func validateSource(source store.BindingSource) error {
	if strings.TrimSpace(source.GatewayID) == "" || strings.TrimSpace(source.PropertyID) == "" || source.ChannelID < 0 || source.DeviceID < 0 {
		return errors.New("绑定来源需完整选择网关、通道、设备和属性")
	}
	return nil
}

// stripEmptySources 过滤未选择网关的空来源行（绑定不是必须的）
func stripEmptySources(sources []store.BindingSource) []store.BindingSource {
	out := make([]store.BindingSource, 0, len(sources))
	for _, source := range sources {
		if strings.TrimSpace(source.GatewayID) == "" {
			continue
		}
		out = append(out, source)
	}
	return out
}

// validateAlarmEvents 校验告警条目：key 唯一、绑定点位（如已配置）存在。
// 点位为空表示该告警暂未配置监测点位，允许保存。
func validateAlarmEvents(events store.Events, properties []store.Property) error {
	seen := make(map[string]bool, len(events))
	for _, event := range events {
		if strings.TrimSpace(event.Key) == "" {
			return errors.New("告警配置：事件 key 不能为空")
		}
		if seen[event.Key] {
			return fmt.Errorf("告警配置：事件 key 重复: %s", event.Key)
		}
		seen[event.Key] = true
		propertyKey := strings.TrimSpace(event.Binding.PropertyKey)
		if propertyKey == "" {
			continue
		}
		if _, ok := findProperty(properties, propertyKey); !ok {
			return fmt.Errorf("告警配置：告警 %s 监测的属性点位 %s 不存在", event.Key, propertyKey)
		}
	}
	return nil
}

