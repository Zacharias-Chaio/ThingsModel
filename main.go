package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"thingsmodel/internal/api"
	"thingsmodel/internal/logging"
	"thingsmodel/internal/runtime"
	"thingsmodel/internal/store"
	"thingsmodel/internal/web"
)

func main() {
	addr := flag.String("addr", ":8090", "HTTP 监听地址")
	tplDir := flag.String("templates", "templats", "模板目录（相对或绝对路径）")
	dbPath := flag.String("db", "data/thingsmodel.db", "SQLite 数据库文件（相对或绝对路径）")
	flag.Parse()

	absTplDir, err := filepath.Abs(*tplDir)
	if err != nil {
		fatal("解析模板目录失败", err)
	}
	// 确保模板目录存在
	if err := os.MkdirAll(absTplDir, 0755); err != nil {
		fatal("创建模板目录失败", err)
	}
	absDBPath, err := filepath.Abs(*dbPath)
	if err != nil {
		fatal("解析数据库路径失败", err)
	}
	if err := os.MkdirAll(filepath.Dir(absDBPath), 0755); err != nil {
		fatal("创建数据库目录失败", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(absDBPath)
	if err != nil {
		fatal("初始化数据库失败", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		fatal("获取数据库连接失败", err)
	}
	defer sqlDB.Close()

	settings, err := store.OpenSettings(db)
	if err != nil {
		fatal("加载平台配置失败", err)
	}
	logRuntime, err := logging.NewRuntime(logging.Config{Logger: settings.Get().Logger})
	if err != nil {
		fatal("初始化日志失败", err)
	}
	defer logRuntime.Close()

	// 启动时扫描加载所有模板
	tplStore := store.NewTemplateStore(absTplDir)
	if n, err := tplStore.Scan(); err != nil {
		slog.Warn("扫描模板目录失败", "error", err)
	} else {
		slog.Info("已加载物模型模板", "count", n, "directory", absTplDir)
	}

	deviceRuntime, err := runtime.NewManager(ctx, settings.Get())
	if err != nil {
		fatal("初始化物模型运行时失败", err)
	}
	defer deviceRuntime.Stop()
	srv := &api.Server{Templates: tplStore, DB: db, Runtime: deviceRuntime, Settings: settings, Logger: logRuntime}
	if err := srv.ReloadRuntime(); err != nil {
		fatal("加载设备配置失败", err)
	}
	router := web.NewRouter(srv)

	httpSrv := &http.Server{
		Addr:         *addr,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// 优雅退出
	go func() {
		slog.Info("物模型配置服务启动", "address", "http://localhost"+*addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal("HTTP 服务异常", err)
		}
	}()

	<-ctx.Done()
	slog.Info("收到退出信号，正在关闭服务")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		slog.Warn("强制关闭", "error", err)
	}
	slog.Info("服务已停止")
}

func fatal(message string, err error) {
	slog.Error(message, "error", err)
	os.Exit(1)
}
