package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

func (s *Server) GetRuntimeDevice(w http.ResponseWriter, r *http.Request) {
	if s.Runtime == nil {
		fail(w, http.StatusServiceUnavailable, "运行数据服务未就绪")
		return
	}
	status, found := s.Runtime.Get(chi.URLParam(r, "id"))
	if !found {
		fail(w, http.StatusNotFound, "设备不存在")
		return
	}
	ok(w, status)
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
	for _, event := range config.Events {
		for _, source := range event.Binding {
			if err := validateSource(source); err != nil {
				return fmt.Errorf("告警 %s: %w", event.Key, err)
			}
		}
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
	eventBindings := make(map[string][]store.BindingSource, len(config.Events))
	for _, event := range config.Events {
		eventBindings[event.Key] = event.Binding
	}

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
	config.Events = make([]store.Event, len(template.Events))
	for index, event := range template.Events {
		event.Binding = eventBindings[event.Key]
		if event.Binding == nil {
			event.Binding = []store.BindingSource{}
		}
		config.Events[index] = event
	}
}

func normalizeDeviceConfig(config *store.DeviceConfig) {
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
		config.Events = []store.Event{}
	}
}

func validatePropertyBinding(property store.Property) error {
	binding := property.Binding
	if binding.Method == "" && len(binding.Sources) == 0 {
		return nil
	}
	if property.Type == "enum" && binding.Method != "EPT" {
		return errors.New("枚举属性只支持 EPT 直接绑定")
	}
	switch binding.Method {
	case "EPT", "NOT":
		if len(binding.Sources) != 1 {
			return fmt.Errorf("%s 需要一个来源", binding.Method)
		}
	case "SUM", "AVG", "MIN", "MAX":
		if len(binding.Sources) < 1 {
			return fmt.Errorf("%s 至少需要一个来源", binding.Method)
		}
	case "AND", "OR":
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
	if binding.DeviceID == "" && binding.PropertyID == "" {
		return nil
	}
	return validateSource(store.BindingSource{DeviceID: binding.DeviceID, PropertyID: binding.PropertyID})
}

func validateSource(source store.BindingSource) error {
	if strings.TrimSpace(source.DeviceID) == "" || strings.TrimSpace(source.PropertyID) == "" {
		return errors.New("设备 ID 和属性 ID 必须同时填写")
	}
	return nil
}
