package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"thingsmodel/internal/api"
	"thingsmodel/internal/store"
	"thingsmodel/internal/web"
)

func main() {
	addr := flag.String("addr", ":8090", "HTTP 监听地址")
	tplDir := flag.String("templates", "templats", "模板目录（相对或绝对路径）")
	flag.Parse()

	absTplDir, err := filepath.Abs(*tplDir)
	if err != nil {
		log.Fatalf("解析模板目录失败: %v", err)
	}
	// 确保模板目录存在
	if err := os.MkdirAll(absTplDir, 0755); err != nil {
		log.Fatalf("创建模板目录失败: %v", err)
	}

	// 启动时扫描加载所有模板
	tplStore := store.NewTemplateStore(absTplDir)
	if n, err := tplStore.Scan(); err != nil {
		log.Printf("[warn] 扫描模板目录失败: %v", err)
	} else {
		log.Printf("[info] 已加载 %d 个物模型模板 (目录: %s)", n, absTplDir)
	}

	srv := &api.Server{Templates: tplStore}
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
		log.Printf("[info] 物模型配置服务启动: http://localhost%s", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务异常: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("[info] 收到退出信号，正在关闭服务...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Printf("[warn] 强制关闭: %v", err)
	}
	log.Println("[info] 服务已停止")
}
