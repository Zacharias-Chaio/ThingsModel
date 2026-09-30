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
	alarmBindings := config.Events.Binding

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
		config.Properties[index] = property
	}
	config.Methods = make([]store.Method, len(template.Methods))
	for index, method := range template.Methods {
		method.Binding = methodBindings[method.Key]
		config.Methods[index] = method
	}
	config.Events.Rule = make([]store.Event, len(template.Events))
	copy(config.Events.Rule, template.Events)
	config.Events.Binding = pruneAlarmBindings(alarmBindings, template.Events)
}

// syncDevicesFromTemplate 把模板的最新定义同步到所有引用该模板的设备实例：
// 重新水合快照（按 key 保留已配置绑定）、剔除已删除告警规则的引用并持久化。
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
	// 告警关联：过滤空行并归一化判断方法（单规则 ept，多规则默认 or）
	config.Events.Binding = normalizeAlarmBindings(config.Events.Binding)
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
	if config.Events.Rule == nil {
		config.Events.Rule = []store.Event{}
	}
}

func validatePropertyBinding(property store.Property) error {
	if property.Mode == store.PropertyModeLogical {
		if len(property.Binding.Sources) > 0 {
			return errors.New("逻辑点位不应绑定来源")
		}
		return nil
	}
	binding := property.Binding
	// 绑定不是必须的：无有效来源视为未绑定
	if len(binding.Sources) == 0 {
		return nil
	}
	if property.Type == "status" && binding.Method != "ept" {
		return errors.New("状态属性只支持 EPT 直接绑定")
	}
	switch binding.Method {
	case "ept", "not":
		if len(binding.Sources) != 1 {
			return fmt.Errorf("%s 需要一个来源", binding.Method)
		}
	case "sum", "avg", "min", "max":
		if len(binding.Sources) < 1 {
			return fmt.Errorf("%s 至少需要一个来源", binding.Method)
		}
	case "and", "or":
		if len(binding.Sources) < 2 {
			return fmt.Errorf("%s 至少需要两个来源", binding.Method)
		}
	default:
		return errors.New("不支持的聚合方法")
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

// validateAlarmEvents 校验告警监测点位关联：点位存在、规则存在且不重复、多规则方法为 and/or。
func validateAlarmEvents(events store.DeviceEvents, properties []store.Property) error {
	ruleKeys := make(map[string]bool, len(events.Rule))
	for _, rule := range events.Rule {
		ruleKeys[rule.Key] = true
	}
	for _, binding := range events.Binding {
		pointEmpty := strings.TrimSpace(binding.PropertyKey) == ""
		if pointEmpty && len(binding.Rules) == 0 {
			continue
		}
		if pointEmpty {
			return errors.New("告警关联：请先选择监测点位")
		}
		if len(binding.Rules) == 0 {
			return errors.New("告警关联：监测点位需关联至少一条告警规则")
		}
		if _, ok := findProperty(properties, binding.PropertyKey); !ok {
			return fmt.Errorf("告警关联：监测属性点位 %s 不存在", binding.PropertyKey)
		}
		seen := make(map[string]bool, len(binding.Rules))
		for _, key := range binding.Rules {
			if !ruleKeys[key] {
				return fmt.Errorf("告警关联：规则 %s 不存在", key)
			}
			if seen[key] {
				return fmt.Errorf("告警关联：规则 %s 重复关联", key)
			}
			seen[key] = true
		}
	}
	return nil
}

// normalizeAlarmBindings 过滤空关联行（点位与规则均未选择），并归一化判断方法：
// 单规则 ept，多规则 and/or（默认 or）。
func normalizeAlarmBindings(bindings []store.AlarmBinding) []store.AlarmBinding {
	out := make([]store.AlarmBinding, 0, len(bindings))
	for _, binding := range bindings {
		rules := make([]string, 0, len(binding.Rules))
		for _, key := range binding.Rules {
			if strings.TrimSpace(key) != "" {
				rules = append(rules, strings.TrimSpace(key))
			}
		}
		binding.Rules = rules
		if strings.TrimSpace(binding.PropertyKey) == "" && len(rules) == 0 {
			continue
		}
		if len(rules) <= 1 {
			binding.Method = "ept"
		} else if binding.Method != "and" && binding.Method != "or" {
			binding.Method = "or"
		}
		out = append(out, binding)
	}
	return out
}

// pruneAlarmBindings 剔除已不在模板中的告警规则引用，并丢弃因此没有有效规则的
// 检测点位；同时归一化判断方法（单规则 ept，多规则 and/or）。
func pruneAlarmBindings(bindings []store.AlarmBinding, rules []store.Event) []store.AlarmBinding {
	valid := make(map[string]bool, len(rules))
	for _, rule := range rules {
		valid[rule.Key] = true
	}
	out := make([]store.AlarmBinding, 0, len(bindings))
	for _, binding := range bindings {
		kept := make([]string, 0, len(binding.Rules))
		for _, key := range binding.Rules {
			if valid[key] {
				kept = append(kept, key)
			}
		}
		if len(kept) == 0 {
			continue
		}
		binding.Rules = kept
		if len(kept) <= 1 {
			binding.Method = "ept"
		} else if binding.Method != "and" && binding.Method != "or" {
			binding.Method = "or"
		}
		out = append(out, binding)
	}
	if out == nil {
		out = []store.AlarmBinding{}
	}
	return out
}
