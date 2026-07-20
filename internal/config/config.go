// Package config 负责运行时目录规划与默认设置。
package config

import (
	"os"
	"path/filepath"
)

// Config 是进程级运行时配置（路径 / 监听地址）。
// 可变的业务设置（刷新间隔等）存放于数据库的 settings 表，见 store 包。
type Config struct {
	Addr    string // 监听地址，如 :8787
	DataDir string // 数据目录
	DBPath  string // SQLite 文件路径
}

// Load 读取环境变量并规划目录：
//
//	ADDR      监听地址          默认 :8787
//	DATA_DIR  数据目录          默认 ./data
//
// 会确保数据目录存在。
func Load() (*Config, error) {
	dataDir := envOr("DATA_DIR", "data")
	abs, err := filepath.Abs(dataDir)
	if err == nil {
		dataDir = abs
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	return &Config{
		Addr:    envOr("ADDR", ":8787"),
		DataDir: dataDir,
		DBPath:  filepath.Join(dataDir, "monitor.db"),
	}, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
