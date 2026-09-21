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
	// 新建设备 或 模板版本与设备记录不一致时，刷新模板快照（保留用户已配置的 binding）
	template, err := s.templateForDevice(config.TemplateCode)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(existingResult.Error, gorm.ErrRecordNotFound) || config.TemplateVersion != template.Version {
		hydrateDeviceSnapshot(&config, template)
	}
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
		if err := validateMethodBinding(method); err != nil {
			return fmt.Errorf("服务 %s: %w", method.Key, err)
		}
	}
	if err := validateAlarmEvents(config.Events); err != nil {
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
	for _, property := range config.Properties {
		propertyBindings[property.Key] = property.Binding
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
		config.Properties[index] = property
	}
	config.Methods = make([]store.Method, len(template.Methods))
	for index, method := range template.Methods {
		method.Binding = methodBindings[method.Key]
		config.Methods[index] = method
	}
	config.Events.Rule = make([]store.Event, len(template.Events))
	copy(config.Events.Rule, template.Events)
	config.Events.Binding = alarmBindings
	if config.Events.Binding == nil {
		config.Events.Binding = []store.AlarmBinding{}
	}
}

func normalizeDeviceConfig(config *store.DeviceConfig) {
	// 绑定不是必须的：过滤未选择网关的空来源行，未选择网关的服务绑定置空
	for i := range config.Properties {
		config.Properties[i].Binding.Sources = stripEmptySources(config.Properties[i].Binding.Sources)
	}
	// 告警关联：过滤空行并归一化判断方法（单规则 ept，多规则默认 or）
	config.Events.Binding = normalizeAlarmBindings(config.Events.Binding)
	for i := range config.Methods {
		if strings.TrimSpace(config.Methods[i].Binding.GatewayID) == "" {
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

func validateMethodBinding(method store.Method) error {
	binding := method.Binding
	if strings.TrimSpace(binding.GatewayID) == "" {
		return nil // 未选择网关 = 未绑定，留空表示暂不启用该服务
	}
	return validateSource(store.BindingSource{GatewayID: binding.GatewayID, ChannelID: binding.ChannelID, DeviceID: binding.DeviceID, PropertyID: binding.PropertyID})
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

// validateAlarmEvents 校验告警检测点位关联：点位完整、规则存在且不重复、多规则方法为 and/or。
func validateAlarmEvents(events store.DeviceEvents) error {
	ruleKeys := make(map[string]bool, len(events.Rule))
	for _, rule := range events.Rule {
		ruleKeys[rule.Key] = true
	}
	for _, binding := range events.Binding {
		pointEmpty := strings.TrimSpace(binding.Point.GatewayID) == ""
		if pointEmpty && len(binding.Rules) == 0 {
			continue
		}
		if pointEmpty {
			return errors.New("告警关联：请先选择检测点位")
		}
		if len(binding.Rules) == 0 {
			return errors.New("告警关联：检测点位需关联至少一条告警规则")
		}
		if err := validateSource(binding.Point); err != nil {
			return fmt.Errorf("告警关联: %w", err)
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
		if strings.TrimSpace(binding.Point.GatewayID) == "" && len(rules) == 0 {
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
