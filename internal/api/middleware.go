package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/yegors/co-atc/pkg/logger"
)

// Middleware 包含自定义中间件函数
type Middleware struct {
	logger *logger.Logger
}

// NewMiddleware 创建一个新的中间件
func NewMiddleware(logger *logger.Logger) *Middleware {
	return &Middleware{
		logger: logger.Named("api-middleware"),
	}
}

// Logger 是用于记录 HTTP 请求的中间件
func (m *Middleware) Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		defer func() {
			m.logger.Debug("HTTP 请求",
				logger.String("method", r.Method),
				logger.String("path", r.URL.Path),
				logger.String("remote_addr", r.RemoteAddr),
				logger.String("user_agent", r.UserAgent()),
				logger.Int("status", ww.Status()),
				logger.Int("bytes", ww.BytesWritten()),
				logger.Duration("duration", time.Since(start)),
			)
		}()

		next.ServeHTTP(ww, r)
	})
}

// CORS 是用于向响应添加 CORS 头部的中间件
func (m *Middleware) CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// 检查来源是否被允许
			allowed := false
			if len(allowedOrigins) == 0 {
				allowed = true
			} else if origin != "" {
				for _, allowedOrigin := range allowedOrigins {
					if allowedOrigin == "*" || allowedOrigin == origin {
						allowed = true
						break
					}
				}
			}

			// 设置 CORS 头部
			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}

			// 处理预检请求
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequestID 是用于将请求 ID 添加到上下文的中间件
func (m *Middleware) RequestID(next http.Handler) http.Handler {
	return middleware.RequestID(next)
}

// Recoverer 是用于从 panic 中恢复的中间件
func (m *Middleware) Recoverer(next http.Handler) http.Handler {
	return middleware.Recoverer(next)
}
