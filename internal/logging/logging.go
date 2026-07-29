// Package logging configures application logging from YAML.
package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
	"gopkg.in/yaml.v3"
)

// Config describes the logger section of config.yaml.
type Config struct {
	Logger LoggerConfig `yaml:"logger"`
}

// LoggerConfig controls log level and destinations.
type LoggerConfig struct {
	Level      string     `yaml:"level"`
	Console    bool       `yaml:"console"`
	File       bool       `yaml:"file"`
	FileConfig FileConfig `yaml:"file_config"`
}

// FileConfig controls rotating file output. Size is measured in megabytes.
type FileConfig struct {
	Filename   string `yaml:"filename"`
	MaxSize    int    `yaml:"max_size"`
	MaxBackups int    `yaml:"max_backups"`
	MaxAge     int    `yaml:"max_age"`
	Compress   bool   `yaml:"compress"`
}

// DefaultConfig returns the built-in logging configuration.
func DefaultConfig() Config {
	return Config{Logger: LoggerConfig{
		Level:   "info",
		Console: true,
		File:    true,
		FileConfig: FileConfig{
			Filename:   "logs/thingsmodel.log",
			MaxSize:    10,
			MaxBackups: 5,
			MaxAge:     30,
			Compress:   true,
		},
	}}
}

// Load reads and validates the YAML configuration at path.
func Load(path string) (Config, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取日志配置 %s: %w", filepath.Clean(path), err)
	}

	config := DefaultConfig()
	if err := yaml.Unmarshal(contents, &config); err != nil {
		return Config{}, fmt.Errorf("解析日志配置 %s: %w", filepath.Clean(path), err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Save validates and writes the configuration as YAML.
func Save(path string, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	contents, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("序列化日志配置: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("创建日志配置目录: %w", err)
	}
	if err := os.WriteFile(path, contents, 0644); err != nil {
		return fmt.Errorf("写入日志配置 %s: %w", filepath.Clean(path), err)
	}
	return nil
}

// Validate checks that the logging configuration is usable.
func (config Config) Validate() error {
	if _, err := parseLevel(config.Logger.Level); err != nil {
		return err
	}
	if !config.Logger.Console && !config.Logger.File {
		return errors.New("日志配置至少要启用终端或文件输出")
	}
	if !config.Logger.File {
		return nil
	}
	fileConfig := config.Logger.FileConfig
	if strings.TrimSpace(fileConfig.Filename) == "" {
		return errors.New("文件日志已启用但 filename 为空")
	}
	if fileConfig.MaxSize <= 0 {
		return errors.New("文件日志 max_size 必须大于 0")
	}
	if fileConfig.MaxBackups < 0 || fileConfig.MaxAge < 0 {
		return errors.New("文件日志 max_backups 和 max_age 不能小于 0")
	}
	return nil
}

// New creates a structured logger and an optional file closer for the configured destinations.
func New(config Config) (*slog.Logger, io.Closer, error) {
	if err := config.Validate(); err != nil {
		return nil, nil, err
	}
	level, _ := parseLevel(config.Logger.Level)
	writers := make([]io.Writer, 0, 2)
	var fileWriter *lumberjack.Logger
	if config.Logger.Console {
		writers = append(writers, os.Stdout)
	}
	if config.Logger.File {
		fileConfig := config.Logger.FileConfig
		if err := os.MkdirAll(filepath.Dir(fileConfig.Filename), 0755); err != nil {
			return nil, nil, fmt.Errorf("创建日志目录: %w", err)
		}
		fileWriter = &lumberjack.Logger{
			Filename:   fileConfig.Filename,
			MaxSize:    fileConfig.MaxSize,
			MaxBackups: fileConfig.MaxBackups,
			MaxAge:     fileConfig.MaxAge,
			Compress:   fileConfig.Compress,
		}
		writers = append(writers, fileWriter)
	}
	return slog.New(slog.NewTextHandler(io.MultiWriter(writers...), &slog.HandlerOptions{Level: level})), fileWriter, nil
}

func parseLevel(value string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToUpper(strings.TrimSpace(value)))); err != nil {
		return 0, fmt.Errorf("无效日志级别 %q，应为 debug、info、warn 或 error", value)
	}
	return level, nil
}
