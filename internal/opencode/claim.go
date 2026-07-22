package opencode

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"

	"opencode-monitor/internal/store"
)

// 页面元素选择器（对应 opencode 工作空间 Go 页面的邀请奖励表与「使用奖励」弹窗）。
const (
	selReferralRows = `[data-slot="referrals-table"] tr[data-status]`
	selViewButton   = `[data-slot="referral-action"] button` // 「查看奖励」
	selDialog       = `[role="dialog"]`
	selUseButton    = `[role="dialog"] button[data-color="primary"]` // 「使用」
)

// ClaimReferral 通过无头浏览器复刻「查看奖励 → 使用」的点击，领取第 index 条邀请奖励。
//
// opencode 的「使用」按钮是 Next.js Server Action，其请求签名藏在前端 JS 里、无法从
// HTML 推导；因此这里直接驱动一个真实（无头）Chromium 带账号登录态打开页面并点击，
// 完全模拟人工操作。首次运行若本机没有 Chromium，rod 会自动下载一份到用户缓存目录。
func ClaimReferral(workspaceID, auth string, index int, ref store.Referral, timeout time.Duration) error {
	if strings.TrimSpace(workspaceID) == "" {
		return fmt.Errorf("工作空间 ID 为空")
	}
	if index < 0 {
		return fmt.Errorf("奖励序号无效")
	}
	// 浏览器流程（导航 + 水合 + 点击 + 等网络空闲）比单纯抓取慢，设下限。
	if timeout < 45*time.Second {
		timeout = 45 * time.Second
	}

	// 找到（或自动下载）Chromium 并启动无头实例。
	// no-sandbox：以 root 或容器内运行时 Chromium 会拒绝开启 sandbox；
	// disable-dev-shm-usage / disable-gpu：容器内 /dev/shm 偏小、无 GPU。
	l := launcher.New().Headless(true).
		Set("no-sandbox").
		Set("disable-gpu").
		Set("disable-dev-shm-usage")
	if path, ok := launcher.LookPath(); ok {
		l = l.Bin(path)
	}
	controlURL, err := l.Launch()
	if err != nil {
		return fmt.Errorf("启动无头浏览器失败：%w\n"+
			"多为缺少系统库（如 libatk-1.0.so.0）或以 root 运行 sandbox 受限。"+
			"Debian/Ubuntu 可执行：apt-get install -y chromium（会带齐依赖库）", err)
	}
	defer l.Cleanup()

	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		return fmt.Errorf("连接浏览器失败：%w", err)
	}
	defer browser.Close()

	// 注入登录态：Cookie 头写入 Cookie；Bearer 令牌写入 Authorization 头。
	if cookies := parseCookies(auth); len(cookies) > 0 {
		if err := browser.SetCookies(cookies); err != nil {
			return fmt.Errorf("设置 Cookie 失败：%w", err)
		}
	}

	url := fmt.Sprintf(workspaceURLFmt, workspaceID)
	var claimErr error // 业务性错误（区别于 rod 的运行时 panic）

	runErr := rod.Try(func() {
		page := browser.MustPage().Timeout(timeout)
		defer page.MustClose()

		if bearer := strings.TrimSpace(auth); strings.HasPrefix(bearer, "Bearer ") {
			page.MustSetExtraHeaders("Authorization", bearer)
		}

		page.MustNavigate(url).MustWaitLoad()
		page.MustWaitStable() // 等待 Next.js 水合完成，按钮才可点击

		rows := page.MustElements(selReferralRows)
		if index >= len(rows) {
			claimErr = fmt.Errorf("页面上未找到第 %d 条奖励（可能已变化，请刷新后重试）", index+1)
			return
		}
		row := rows[index]
		if status := row.MustEval(`() => this.getAttribute('data-status')`).Str(); status != "available" {
			claimErr = fmt.Errorf("该奖励在页面上已不可领取（状态：%s），请刷新后重试", status)
			return
		}

		// 点「查看奖励」弹出确认框。
		row.MustElement(selViewButton).MustClick()

		// 等确认框出现，点「使用」，并等待其触发的网络请求完成。
		page.MustElement(selDialog)
		useBtn := page.MustElement(selUseButton)
		wait := page.MustWaitRequestIdle()
		useBtn.MustClick()
		wait()
		page.MustWaitStable()

		// 复核该行是否已变为 applied（领取成功）。
		fresh := page.MustElements(selReferralRows)
		if index < len(fresh) {
			if s := fresh[index].MustEval(`() => this.getAttribute('data-status')`).Str(); s != "applied" {
				claimErr = fmt.Errorf("已点击「使用」但页面状态未变为已使用（当前：%s），请刷新确认", s)
			}
		}
	})
	if runErr != nil {
		if claimErr != nil {
			return claimErr
		}
		return fmt.Errorf("领取过程出错：%w", runErr)
	}
	return claimErr
}

// parseCookies 把 "k=v; k2=v2" 形式的 Cookie 头解析为浏览器 Cookie；
// 若 auth 为空或是 Bearer 令牌则返回 nil。
func parseCookies(auth string) []*proto.NetworkCookieParam {
	auth = strings.TrimSpace(auth)
	if auth == "" || strings.HasPrefix(auth, "Bearer ") {
		return nil
	}
	var out []*proto.NetworkCookieParam
	for _, part := range strings.Split(auth, ";") {
		part = strings.TrimSpace(part)
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			continue
		}
		out = append(out, &proto.NetworkCookieParam{
			Name:   strings.TrimSpace(part[:eq]),
			Value:  strings.TrimSpace(part[eq+1:]),
			Domain: ".opencode.ai",
			Path:   "/",
		})
	}
	return out
}
