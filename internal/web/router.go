package web

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"thingsmodel/internal/api"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

//go:embed all:static
var staticFS embed.FS

// NewRouter 组装路由：/api/* 走 REST，其他走静态单页。
func NewRouter(srv *api.Server) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(accessLog)
	r.Use(recoverer)

	// REST API
	r.Route("/api", func(r chi.Router) {
		r.Get("/templates", srv.ListTemplates)
		r.Post("/templates", srv.SaveTemplate)
		r.Post("/templates/scan", srv.ScanTemplates)
		r.Get("/templates/{code}", srv.GetTemplate)
		r.Delete("/templates/{code}", srv.DeleteTemplate)
		r.Get("/devices", srv.ListDevices)
		r.Post("/devices", srv.SaveDevice)
		r.Get("/devices/{id}", srv.GetDevice)
		r.Delete("/devices/{id}", srv.DeleteDevice)
		r.Get("/runtime/devices", srv.ListRuntimeDevices)
		r.Get("/runtime/devices/{id}", srv.GetRuntimeDevice)
	})

	// 静态资源（嵌入式）
	staticSub, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(staticSub))
	r.Handle("/*", fileServer)

	// 根路径回退到 index.html（单页应用）
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})

	return r
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		writer := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(writer, r)
		slog.Info("HTTP 请求完成",
			"method", r.Method,
			"path", r.URL.Path,
			"status", writer.Status(),
			"bytes", writer.BytesWritten(),
			"duration", time.Since(started),
			"request_id", middleware.GetReqID(r.Context()),
			"remote_addr", r.RemoteAddr,
		)
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("HTTP 请求发生 panic",
					"error", recovered,
					"request_id", middleware.GetReqID(r.Context()),
					"stack", string(debug.Stack()),
				)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
