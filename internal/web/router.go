package web

import (
	"embed"
	"io/fs"
	"net/http"

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
	r.Use(middleware.Recoverer)
	r.Use(requestLog)

	// REST API
	r.Route("/api", func(r chi.Router) {
		r.Get("/templates", srv.ListTemplates)
		r.Post("/templates", srv.SaveTemplate)
		r.Post("/templates/scan", srv.ScanTemplates)
		r.Get("/templates/{code}", srv.GetTemplate)
		r.Delete("/templates/{code}", srv.DeleteTemplate)
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

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 简单请求日志，避免引入额外依赖
		next.ServeHTTP(w, r)
	})
}
