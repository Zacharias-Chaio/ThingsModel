package api

import (
	"encoding/json"
	"errors"
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
	if err := s.Templates.Save(&t); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, t)
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
