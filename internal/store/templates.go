package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// TemplateStore 基于文件系统的模板存储。
// 模板以 JSON 文件形式存放在 templats/ 目录，软件启动时全量扫描加载。
type TemplateStore struct {
	dir  string
	mu   sync.RWMutex
	data map[string]*Template // key = Code
}

// NewTemplateStore 创建模板存储，dir 为 templats 目录绝对路径。
func NewTemplateStore(dir string) *TemplateStore {
	return &TemplateStore{dir: dir, data: make(map[string]*Template)}
}

// Scan 扫描 templats 目录下所有 .json 文件并加载到内存。
// 已存在的模板（按 Code 去重）会被覆盖，保证最新。
func (s *TemplateStore) Scan() (int, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, fmt.Errorf("读取模板目录失败: %w", err)
	}

	loaded := make(map[string]*Template)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		// 跳过文档类文件（模板说明.md 不是 .json，天然忽略）
		path := filepath.Join(s.dir, e.Name())
		t, err := loadTemplateFile(path)
		if err != nil {
			// 单个文件加载失败不中断整体扫描，记录后继续
			fmt.Printf("[store] 跳过模板文件 %s: %v\n", e.Name(), err)
			continue
		}
		if t.Code == "" {
			t.Code = strings.TrimSuffix(e.Name(), ".json")
		}
		loaded[t.Code] = t
	}

	s.mu.Lock()
	s.data = loaded
	count := len(loaded)
	s.mu.Unlock()
	return count, nil
}

func loadTemplateFile(path string) (*Template, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Template
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// List 返回所有模板，按 Code 升序。
func (s *TemplateStore) List() []*Template {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Template, 0, len(s.data))
	for _, t := range s.data {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// Get 按 Code 获取模板。
func (s *TemplateStore) Get(code string) (*Template, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.data[code]
	if !ok {
		return nil, ErrNotFound
	}
	// 返回副本，避免外部修改内部状态
	cp := *t
	return &cp, nil
}

// Save 保存模板：写文件 + 更新内存索引。Code 为空返回错误。
func (s *TemplateStore) Save(t *Template) error {
	if t.Code == "" {
		return ErrEmptyCode
	}
	// 文件名使用 Code（替换非法字符），保留 .json 后缀
	name := sanitizeFileName(t.Code) + ".json"
	path := filepath.Join(s.dir, name)

	raw, err := json.MarshalIndent(t, "", "    ")
	if err != nil {
		return fmt.Errorf("序列化模板失败: %w", err)
	}
	if err := os.WriteFile(path, raw, 0644); err != nil {
		return fmt.Errorf("写入模板文件失败: %w", err)
	}

	s.mu.Lock()
	s.data[t.Code] = cloneTemplate(t)
	s.mu.Unlock()
	return nil
}

// cloneTemplate 返回模板的深拷贝，避免外部修改影响内存存储。
// 优先用 JSON 序列化保证切片字段独立；序列化失败时退回浅拷贝防止 nil 解引用。
func cloneTemplate(t *Template) *Template {
	if t == nil {
		return nil
	}
	raw, err := json.Marshal(t)
	if err == nil {
		var cp Template
		if err := json.Unmarshal(raw, &cp); err == nil {
			return &cp
		}
	}
	cp := *t
	return &cp
}

// Delete 按 Code 删除模板：删文件 + 删内存索引。
func (s *TemplateStore) Delete(code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[code]; !ok {
		return ErrNotFound
	}
	name := sanitizeFileName(code) + ".json"
	path := filepath.Join(s.dir, name)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除模板文件失败: %w", err)
	}
	delete(s.data, code)
	return nil
}

var (
	ErrNotFound  = errors.New("模板不存在")
	ErrEmptyCode = errors.New("模板编码不能为空")
)

// sanitizeFileName 替换文件名中不安全字符，避免路径穿越。
func sanitizeFileName(s string) string {
	r := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
	)
	return r.Replace(s)
}
