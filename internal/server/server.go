// Package server 提供 HTTP 接口与前端页面。
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"opencode-monitor/internal/proxy"
	"opencode-monitor/internal/store"
	"opencode-monitor/web"
)

type Server struct {
	store  *store.Store
	proxy  *proxy.Proxy
	reload chan struct{} // 设置变更后通知自动刷新循环重置计时
}

func New(st *store.Store) *Server {
	return &Server{store: st, proxy: proxy.New(st), reload: make(chan struct{}, 1)}
}

// Handler 返回配置好路由的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 页面
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /settings", s.page("settings.html"))
	mux.Handle("GET /static/", http.FileServerFS(web.Files))

	// 管理 API
	mux.HandleFunc("GET /api/accounts", s.handleList)
	mux.HandleFunc("POST /api/accounts", s.handleAdd)
	mux.HandleFunc("POST /api/accounts/bulk", s.handleBulk)
	mux.HandleFunc("PUT /api/accounts/{id}", s.handleUpdate)
	mux.HandleFunc("DELETE /api/accounts/{id}", s.handleDelete)
	mux.HandleFunc("POST /api/accounts/{id}/refresh", s.handleRefreshOne)
	mux.HandleFunc("POST /api/refresh", s.handleRefreshAll)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	mux.HandleFunc("GET /api/models", s.handleModels)
	mux.HandleFunc("POST /api/models", s.handleAddModel)
	mux.HandleFunc("DELETE /api/models/{id}", s.handleDeleteModel)

	// 转发接口（/v1/...）
	s.proxy.Register(mux)

	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.page("index.html")(w, r)
}

func (s *Server) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := web.Files.ReadFile(name)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	}
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	accs, err := s.store.All()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, viewsOf(accs))
}

func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Account, Password, AuxEmail, WorkspaceID, Auth, APIKey string
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "无效请求", 400)
		return
	}
	acc := &store.Account{
		Account:     strings.TrimSpace(in.Account),
		Password:    strings.TrimSpace(in.Password),
		AuxEmail:    strings.TrimSpace(in.AuxEmail),
		WorkspaceID: strings.TrimSpace(in.WorkspaceID),
		Auth:        strings.TrimSpace(in.Auth),
		APIKey:      strings.TrimSpace(in.APIKey),
	}
	// 账号字段若粘贴了 a----b----c，自动拆分补全空白字段。
	if strings.Contains(acc.Account, "----") {
		p := splitCred(acc.Account)
		acc.Account = p[0]
		if acc.Password == "" {
			acc.Password = p[1]
		}
		if acc.AuxEmail == "" {
			acc.AuxEmail = p[2]
		}
	}
	if acc.WorkspaceID == "" {
		http.Error(w, "工作空间 ID 必填", 400)
		return
	}
	if err := s.store.Add(acc); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	go s.RefreshAccount(acc.ID)
	writeJSON(w, acc.View())
}

func (s *Server) handleBulk(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "无效请求", 400)
		return
	}
	var added []string
	for _, line := range strings.Split(in.Text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		acc := parseBulkLine(line)
		if acc == nil || acc.WorkspaceID == "" {
			continue
		}
		if s.store.Add(acc) == nil {
			added = append(added, acc.ID)
		}
	}
	for _, id := range added {
		go s.RefreshAccount(id)
	}
	writeJSON(w, map[string]int{"added": len(added)})
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "无效请求", 400)
		return
	}
	acc, err := s.store.Update(r.PathValue("id"), func(a *store.Account) {
		set(&a.Account, in, "account")
		set(&a.Password, in, "password")
		set(&a.AuxEmail, in, "auxEmail")
		set(&a.WorkspaceID, in, "workspaceID")
		set(&a.Auth, in, "auth")
		set(&a.APIKey, in, "apiKey")
	})
	if err != nil {
		httpErr(w, err)
		return
	}
	go s.RefreshAccount(acc.ID)
	writeJSON(w, acc.View())
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(r.PathValue("id")); err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleRefreshOne(w http.ResponseWriter, r *http.Request) {
	acc, err := s.RefreshAccount(r.PathValue("id"))
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, acc.View())
}

func (s *Server) handleRefreshAll(w http.ResponseWriter, r *http.Request) {
	s.RefreshAll()
	accs, _ := s.store.All()
	writeJSON(w, viewsOf(accs))
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.GetSettings())
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in store.Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "无效请求", 400)
		return
	}
	if err := validateSettings(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.store.SaveSettings(in); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.signalReload() // 让自动刷新循环用新间隔重置计时
	writeJSON(w, in)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	ms, err := s.store.Models()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if ms == nil {
		ms = []store.Model{}
	}
	writeJSON(w, ms)
}

func (s *Server) handleAddModel(w http.ResponseWriter, r *http.Request) {
	var m store.Model
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, "无效请求", 400)
		return
	}
	m.ID = strings.TrimSpace(m.ID)
	if m.ID == "" {
		http.Error(w, "模型 ID 必填", 400)
		return
	}
	if err := s.store.AddModel(m); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, m)
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteModel(r.PathValue("id")); err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// validateSettings 校验并规整设置字段。
func validateSettings(in *store.Settings) error {
	if _, err := time.ParseDuration(in.RefreshInterval); err != nil {
		return errors.New("自动刷新间隔格式错误（如 30m、1h、90s）")
	}
	if _, err := time.ParseDuration(in.RequestTimeout); err != nil {
		return errors.New("请求超时格式错误（如 25s）")
	}
	if in.Concurrency < 1 {
		in.Concurrency = 1
	} else if in.Concurrency > 32 {
		in.Concurrency = 32
	}
	if in.WarnPercent < 1 || in.WarnPercent > 100 {
		in.WarnPercent = 80
	}
	if in.ExpirySoonDays < 0 {
		in.ExpirySoonDays = 0
	}
	if in.MaxRetries < 0 {
		in.MaxRetries = 0
	} else if in.MaxRetries > 10 {
		in.MaxRetries = 10
	}
	in.ProxyKey = strings.TrimSpace(in.ProxyKey)
	return nil
}

// ---- helpers ----

// parseBulkLine 解析批量导入的一行。
// 竖线格式：凭据 | 工作空间ID | Auth | APIKey
// 其中凭据为 账号----密码----辅助邮箱。
// 无竖线时整行按 ---- 拆成 账号----密码----辅助邮箱----工作空间ID----Auth----APIKey。
func parseBulkLine(line string) *store.Account {
	if strings.Contains(line, "|") {
		segs := strings.Split(line, "|")
		for i := range segs {
			segs[i] = strings.TrimSpace(segs[i])
		}
		c := splitCred(segs[0])
		acc := &store.Account{Account: c[0], Password: c[1], AuxEmail: c[2]}
		if len(segs) > 1 {
			acc.WorkspaceID = segs[1]
		}
		if len(segs) > 2 {
			acc.Auth = segs[2]
		}
		if len(segs) > 3 {
			acc.APIKey = segs[3]
		}
		return acc
	}
	p := strings.Split(line, "----")
	for i := range p {
		p[i] = strings.TrimSpace(p[i])
	}
	acc := &store.Account{}
	assign := []*string{&acc.Account, &acc.Password, &acc.AuxEmail, &acc.WorkspaceID, &acc.Auth, &acc.APIKey}
	for i := 0; i < len(assign) && i < len(p); i++ {
		*assign[i] = p[i]
	}
	return acc
}

func splitCred(s string) [3]string {
	var out [3]string
	parts := strings.Split(s, "----")
	for i := 0; i < 3 && i < len(parts); i++ {
		out[i] = strings.TrimSpace(parts[i])
	}
	return out
}

func viewsOf(accs []*store.Account) []store.AccountView {
	out := make([]store.AccountView, 0, len(accs))
	for _, a := range accs {
		out = append(out, a.View())
	}
	return out
}

func set(dst *string, m map[string]string, key string) {
	if v, ok := m[key]; ok {
		*dst = strings.TrimSpace(v)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "未找到", 404)
		return
	}
	http.Error(w, err.Error(), 500)
}
