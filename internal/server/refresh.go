package server

import (
	"log"
	"sync"
	"time"

	"opencode-monitor/internal/opencode"
	"opencode-monitor/internal/store"
)

// RefreshAccount 抓取单个账号并把结果写回。
func (s *Server) RefreshAccount(id string) (*store.Account, error) {
	acc, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
	timeout := parseDur(s.store.GetSettings().RequestTimeout, 25*time.Second)
	res, ferr := opencode.Fetch(acc.WorkspaceID, acc.Auth, timeout)
	now := time.Now()

	return s.store.Update(id, func(a *store.Account) {
		a.LastChecked = &now
		if ferr != nil {
			a.Status = "error"
			a.Error = ferr.Error()
			return
		}
		a.Status = "ok"
		a.Error = ""
		a.ReportEmail = res.ReportEmail
		a.Subscribed = res.Subscribed
		a.Rolling = res.Rolling
		a.Weekly = res.Weekly
		a.Monthly = res.Monthly
		// 到期时间 = 抓取时刻 + 最后一档额度的重置秒数。
		if u := res.Expiry(); u != nil {
			exp := now.Add(time.Duration(u.ResetInSec) * time.Second)
			a.ExpiresAt = &exp
		}
	})
}

// RefreshAll 并发刷新全部账号，并发数取自设置。
func (s *Server) RefreshAll() {
	accs, err := s.store.All()
	if err != nil {
		log.Printf("刷新全部失败: %v", err)
		return
	}
	conc := s.store.GetSettings().Concurrency
	if conc < 1 {
		conc = 1
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, a := range accs {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := s.RefreshAccount(id); err != nil {
				log.Printf("刷新账号 %s 失败: %v", id, err)
			}
		}(a.ID)
	}
	wg.Wait()
}

// StartAutoRefresh 启动后台自动刷新循环。会在设置变更时重置计时。
func (s *Server) StartAutoRefresh() {
	go func() {
		time.Sleep(3 * time.Second)
		s.RefreshAll()
		for {
			d := parseDur(s.store.GetSettings().RefreshInterval, 30*time.Minute)
			t := time.NewTimer(d)
			select {
			case <-t.C:
				s.RefreshAll()
			case <-s.reload:
				t.Stop() // 设置已变更，循环重新读取间隔
			}
		}
	}()
}

func (s *Server) signalReload() {
	select {
	case s.reload <- struct{}{}:
	default:
	}
}

func parseDur(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}
