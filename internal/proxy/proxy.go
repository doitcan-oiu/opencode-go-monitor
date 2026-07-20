// Package proxy 把 OpenAI / Anthropic 兼容请求转发到 opencode Go 官方接口，
// 并在多账号之间按「用量最少」负载均衡地挑选 API Key；失败时自动改用其它 Key 重试。
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"opencode-monitor/internal/store"
)

const upstreamHost = "opencode.ai"

const maxBodyBytes = 32 << 20 // 转发请求体上限 32MB（需缓存以便重试）

// 复制响应/请求时应剔除的逐跳首部。
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
}

type Proxy struct {
	store  *store.Store
	bal    *Balancer
	client *http.Client
}

func New(st *store.Store) *Proxy {
	return &Proxy{
		store: st,
		bal:   NewBalancer(st),
		client: &http.Client{
			// 不设整体超时，保证长时流式响应；由 Transport 控制连接级超时。
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 120 * time.Second,
				ExpectContinueTimeout: time.Second,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
			},
		},
	}
}

// Register 注册转发相关路由。
//
//	GET /v1/models   —— 本地模型清单
//	POST/GET /v1/*   —— 路径镜像转发到 https://opencode.ai/zen/go/v1/*
func (p *Proxy) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/models", p.withAuth(p.handleModels))
	// 分方法注册以避免与 "GET /" 页面路由冲突（Go 1.22 mux 规则）。
	mux.HandleFunc("POST /v1/", p.withAuth(p.handleForward))
	mux.HandleFunc("GET /v1/", p.withAuth(p.handleForward))
}

// handleForward 缓存请求体后，最多尝试 (MaxRetries+1) 个不同的 Key。
// 传输错误或上游返回 429/5xx 视为失败，改用下一个 Key；用尽后把最后一次
// 上游响应（若有）如实回给客户端。
func (p *Proxy) handleForward(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "读取请求体失败")
		return
	}
	maxAttempts := p.store.GetSettings().MaxRetries + 1

	var (
		used     []string
		attempts int
		lastErr  string
		lastResp *http.Response
	)
	for {
		if attempts >= maxAttempts {
			break
		}
		acc := p.bal.PickExcluding(used)
		if acc == nil {
			break
		}
		used = append(used, acc.ID)
		attempts++

		resp, ferr := p.forwardOnce(r, body, acc)
		if ferr != nil {
			lastErr = ferr.Error()
			continue
		}
		if retryable(resp.StatusCode) {
			lastErr = fmt.Sprintf("上游返回 %d", resp.StatusCode)
			if lastResp != nil {
				lastResp.Body.Close()
			}
			lastResp = resp // 暂存为兜底，若无更多 Key 可用则回给客户端
			continue
		}
		if lastResp != nil {
			lastResp.Body.Close()
		}
		p.copyResponse(w, resp)
		return
	}

	if lastResp != nil {
		p.copyResponse(w, lastResp)
		return
	}
	if len(used) == 0 {
		writeErr(w, http.StatusServiceUnavailable, "no_api_key", "没有可用的 API Key（请在账号中填写，并确认未到期/未超额）")
		return
	}
	writeErr(w, http.StatusBadGateway, "upstream_error", "已尝试 "+fmt.Sprint(len(used))+" 个 Key 均失败: "+lastErr)
}

// retryable 报告该上游状态码是否应改用其它 Key 重试。
// 401/403（Key 失效或被封）、408、429（限流/超额）以及 5xx 视为 Key 级失败，
// 值得换 Key 重试；400/404/422 等请求级错误对所有 Key 相同，不重试。
func retryable(code int) bool {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return code >= 500
}

// forwardOnce 用指定账号向 opencode 上游发起一次请求（路径镜像）。
func (p *Proxy) forwardOnce(r *http.Request, body []byte, acc *store.Account) (*http.Response, error) {
	// /v1/chat/completions -> https://opencode.ai/zen/go/v1/chat/completions
	url := "https://" + upstreamHost + "/zen/go" + r.URL.Path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, vv := range r.Header {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		switch http.CanonicalHeaderKey(k) {
		case "Authorization", "X-Api-Key", "Accept-Encoding", "Host", "Content-Length":
			continue // 由我们改写或由 client 重新计算
		}
		req.Header[k] = vv
	}
	req.Header.Set("Authorization", "Bearer "+acc.APIKey)
	if strings.HasSuffix(r.URL.Path, "/messages") {
		req.Header.Set("X-Api-Key", acc.APIKey) // Anthropic 风格双保险
	}
	return p.client.Do(req)
}

// copyResponse 把上游响应透传给客户端，边读边刷新以支持 SSE 流式。
func (p *Proxy) copyResponse(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	h := w.Header()
	for k, vv := range resp.Header {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vv {
			h.Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// handleModels 以 OpenAI 格式返回本地模型清单。
func (p *Proxy) handleModels(w http.ResponseWriter, r *http.Request) {
	ms, err := p.store.Models()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	data := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		data = append(data, map[string]any{
			"id": m.ID, "object": "model", "created": 0, "owned_by": "opencode-go",
		})
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}

// withAuth 在设置了转发 Key 时校验请求；为空则放行。
func (p *Proxy) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := p.store.GetSettings().ProxyKey
		if key != "" && !tokenMatches(r, key) {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "转发 Key 无效")
			return
		}
		next(w, r)
	}
}

func tokenMatches(r *http.Request, key string) bool {
	if h := r.Header.Get("Authorization"); h != "" {
		if strings.TrimSpace(strings.TrimPrefix(h, "Bearer")) == key || h == key {
			return true
		}
	}
	return r.Header.Get("X-Api-Key") == key
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": typ},
	})
}
