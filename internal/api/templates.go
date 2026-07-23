package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"thingsmodel/internal/store"

	"github.com/go-chi/chi/v5"
)

// ListTemplates GET /api/templates — 返回全部模板。
func (s *Server) ListTemplates(w http.ResponseWriter, r *http.Request) {
	ok(w, s.Templates.List())
}

// GetTemplate GET /api/templates/{code} — 返回单个模板。
func (s *Server) GetTemplate(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	if code == "" {
		fail(w, http.StatusBadRequest, "缺少 code")
		return
	}
	t, err := s.Templates.Get(code)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(w, http.StatusNotFound, "模板不存在")
			return
		}
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, t)
}

// SaveTemplate POST /api/templates — 创建或更新模板。
// Code 作为唯一键：已存在则覆盖更新，不存在则新增。
func (s *Server) SaveTemplate(w http.ResponseWriter, r *http.Request) {
	var t store.Template
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		fail(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if strings.TrimSpace(t.Code) == "" {
		fail(w, http.StatusBadRequest, "模板编码 code 不能为空")
		return
	}
	if strings.TrimSpace(t.Name) == "" {
		fail(w, http.StatusBadRequest, "模板名称 name 不能为空")
		return
	}
	// 规整化切片，避免 nil 引起前端处理麻烦
	if t.Properties == nil {
		t.Properties = []store.Property{}
	}
	if t.Methods == nil {
		t.Methods = []store.Method{}
	}
	if t.Events == nil {
		t.Events = []store.Event{}
	}
	if err := validateTemplate(&t); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	// 模板中不应包含 binding（binding 只在设备实例化时填入）
	for i := range t.Properties {
		t.Properties[i].Binding = store.Binding{}
	}
	for i := range t.Methods {
		t.Methods[i].Binding = store.MethodBinding{}
	}
	for i := range t.Events {
		t.Events[i].Binding = nil
	}
	if err := s.Templates.Save(&t); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, t)
}

// validateTemplate 校验模板结构：key/number 唯一性、类型合法性、enum 必须有描述。
func validateTemplate(t *store.Template) error {
	propKeys := make(map[string]bool)
	propNumbers := make(map[int]bool)
	for _, p := range t.Properties {
		if strings.TrimSpace(p.Key) == "" {
			return errors.New("属性标识符 key 不能为空")
		}
		if propKeys[p.Key] {
			return fmt.Errorf("属性 key 重复: %s", p.Key)
		}
		if propNumbers[p.Number] {
			return fmt.Errorf("属性 number 重复: %d", p.Number)
		}
		propKeys[p.Key] = true
		propNumbers[p.Number] = true
		if p.Type != "enum" && p.Type != "number" {
			return fmt.Errorf("属性 %s 类型无效: %s（只允许 enum 或 number）", p.Key, p.Type)
		}
		if p.Type == "enum" && len(p.Description) == 0 {
			return fmt.Errorf("属性 %s 为 enum 类型但缺少枚举描述", p.Key)
		}
	}
	mtdKeys := make(map[string]bool)
	mtdNumbers := make(map[int]bool)
	for _, m := range t.Methods {
		if strings.TrimSpace(m.Key) == "" {
			return errors.New("方法标识符 key 不能为空")
		}
		if mtdKeys[m.Key] {
			return fmt.Errorf("方法 key 重复: %s", m.Key)
		}
		if mtdNumbers[m.Number] {
			return fmt.Errorf("方法 number 重复: %d", m.Number)
		}
		mtdKeys[m.Key] = true
		mtdNumbers[m.Number] = true
		if m.Type != "enum" && m.Type != "number" {
			return fmt.Errorf("方法 %s 类型无效: %s（只允许 enum 或 number）", m.Key, m.Type)
		}
		if m.Type == "enum" && len(m.Descriptions) == 0 {
			return fmt.Errorf("方法 %s 为 enum 类型但缺少枚举描述", m.Key)
		}
	}
	evtKeys := make(map[string]bool)
	evtNumbers := make(map[int]bool)
	for _, e := range t.Events {
		if strings.TrimSpace(e.Key) == "" {
			return errors.New("事件标识符 key 不能为空")
		}
		if evtKeys[e.Key] {
			return fmt.Errorf("事件 key 重复: %s", e.Key)
		}
		if evtNumbers[e.Number] {
			return fmt.Errorf("事件 number 重复: %d", e.Number)
		}
		evtKeys[e.Key] = true
		evtNumbers[e.Number] = true
		if e.Type != "equal" && e.Type != "upper" && e.Type != "lower" {
			return fmt.Errorf("事件 %s 类型无效: %s（只允许 equal/upper/lower）", e.Key, e.Type)
		}
		if e.Level < 0 || e.Level > 3 {
			return fmt.Errorf("事件 %s 级别无效: %d（只允许 0-3）", e.Key, e.Level)
		}
	}
	return nil
}

// DeleteTemplate DELETE /api/templates/{code} — 删除模板。
func (s *Server) DeleteTemplate(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	if code == "" {
		fail(w, http.StatusBadRequest, "缺少 code")
		return
	}
	if err := s.Templates.Delete(code); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(w, http.StatusNotFound, "模板不存在")
			return
		}
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, map[string]string{"code": code})
}

// ScanTemplates POST /api/templates/scan — 重新扫描 templats 目录。
func (s *Server) ScanTemplates(w http.ResponseWriter, r *http.Request) {
	n, err := s.Templates.Scan()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, map[string]int{"count": n})
}
