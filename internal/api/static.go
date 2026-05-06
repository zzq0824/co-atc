package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/yegors/co-atc/pkg/logger"
)

// StaticFileHandler 动态提供静态文件,不进行缓存
type StaticFileHandler struct {
	staticDir string
	logger    *logger.Logger
}

// NewStaticFileHandler 创建一个新的静态文件处理器
func NewStaticFileHandler(staticDir string, logger *logger.Logger) *StaticFileHandler {
	return &StaticFileHandler{
		staticDir: staticDir,
		logger:    logger.Named("static-handler"),
	}
}

// ServeHTTP 动态提供静态文件
func (h *StaticFileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 清理路径以防止目录遍历攻击
	path := filepath.Clean(r.URL.Path)

	// 移除前导斜杠
	if strings.HasPrefix(path, "/") {
		path = path[1:]
	}

	// 如果路径为空,提供 index.html
	if path == "" {
		path = "index.html"
	}

	// 构建完整文件路径
	fullPath := filepath.Join(h.staticDir, path)

	// 确保文件位于静态目录内(安全检查)
	absStaticDir, err := filepath.Abs(h.staticDir)
	if err != nil {
		h.logger.Error("获取静态目录的绝对路径失败", logger.Error(err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	absFullPath, err := filepath.Abs(fullPath)
	if err != nil {
		h.logger.Error("获取请求文件的绝对路径失败", logger.Error(err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if !strings.HasPrefix(absFullPath, absStaticDir) {
		h.logger.Warn("尝试目录遍历攻击",
			logger.String("requested_path", path),
			logger.String("full_path", absFullPath),
			logger.String("static_dir", absStaticDir))
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// 检查文件是否存在
	fileInfo, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			// 如果是不带尾部斜杠的目录请求,尝试 index.html
			if !strings.HasSuffix(path, "/") {
				indexPath := filepath.Join(fullPath, "index.html")
				if _, indexErr := os.Stat(indexPath); indexErr == nil {
					fullPath = indexPath
					fileInfo, err = os.Stat(fullPath)
				}
			}

			if err != nil {
				h.logger.Debug("未找到文件", logger.String("path", fullPath))
				http.NotFound(w, r)
				return
			}
		} else {
			h.logger.Error("获取文件状态失败", logger.Error(err), logger.String("path", fullPath))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}

	// 不直接提供目录
	if fileInfo.IsDir() {
		// 尝试从目录提供 index.html
		indexPath := filepath.Join(fullPath, "index.html")
		if _, err := os.Stat(indexPath); err == nil {
			fullPath = indexPath
		} else {
			h.logger.Debug("不允许目录列表", logger.String("path", fullPath))
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	// 设置头部以阻止缓存(用于动态服务)
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")

	// 提供文件
	h.logger.Debug("提供静态文件",
		logger.String("requested_path", r.URL.Path),
		logger.String("file_path", fullPath))

	http.ServeFile(w, r, fullPath)
}
