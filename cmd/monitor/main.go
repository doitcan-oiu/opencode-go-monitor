// Command monitor 启动 opencode Go 额度监控服务。
package main

import (
	"log"
	"net/http"

	"opencode-monitor/internal/config"
	"opencode-monitor/internal/server"
	"opencode-monitor/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()

	srv := server.New(st)
	srv.StartAutoRefresh()

	s := st.GetSettings()
	log.Printf("opencode 额度监控已启动: http://localhost%s", cfg.Addr)
	log.Printf("数据库: %s   自动刷新: %s", cfg.DBPath, s.RefreshInterval)
	log.Fatal(http.ListenAndServe(cfg.Addr, srv.Handler()))
}
