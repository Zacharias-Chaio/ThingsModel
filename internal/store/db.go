package store

import (
	"fmt"
	"path/filepath"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// Open opens the embedded SQLite database and applies schema migrations.
func Open(path string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败 %s: %w", filepath.Clean(path), err)
	}
	if err := db.AutoMigrate(&Device{}); err != nil {
		return nil, fmt.Errorf("迁移数据库失败: %w", err)
	}
	return db, nil
}
