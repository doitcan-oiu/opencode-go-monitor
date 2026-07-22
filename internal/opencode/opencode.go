// Package opencode 抓取并解析 opencode Go 工作空间页面的额度信息。
package opencode

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opencode-monitor/internal/store"
)

const workspaceURLFmt = "https://opencode.ai/workspace/%s/go"

// 用量对象形如： rollingUsage: $R[35] = { status: "ok", resetInSec: 13157, usagePercent: 1 }
// 对象内部为扁平结构，先抓 {...} 再逐字段解析，兼容压缩与不同字段顺序。
var (
	reRolling   = regexp.MustCompile(`rollingUsage:\s*(?:\$R\[\d+\]\s*=\s*)?\{([^}]*)\}`)
	reWeekly    = regexp.MustCompile(`weeklyUsage:\s*(?:\$R\[\d+\]\s*=\s*)?\{([^}]*)\}`)
	reMonthly   = regexp.MustCompile(`monthlyUsage:\s*(?:\$R\[\d+\]\s*=\s*)?\{([^}]*)\}`)
	reStatus    = regexp.MustCompile(`status:\s*"([^"]*)"`)
	reReset     = regexp.MustCompile(`resetInSec:\s*(-?\d+)`)
	rePercent   = regexp.MustCompile(`usagePercent:\s*(-?\d+)`)
	reUserEmail = regexp.MustCompile(`\$R\[28\]\(\s*\$R\[\d+\]\s*,\s*"([A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+)"\s*\)`)
	reSubbed    = regexp.MustCompile(`已订阅|subscriptionPlan|liteSubscriptionID`)

	// 邀请奖励表解析：先框定表区域，再逐行取 data-status / data-source 及各字段。
	reReferralsTable = regexp.MustCompile(`(?s)data-slot="referrals-table".*?</table>`)
	reReferralRow    = regexp.MustCompile(`(?s)<tr\b[^>]*\bdata-status="[^"]*"[^>]*>.*?</tr>`)
	reRefStatus      = regexp.MustCompile(`data-status="([^"]*)"`)
	reRefSource      = regexp.MustCompile(`data-source="([^"]*)"`)
	reRefAmount      = regexp.MustCompile(`data-slot="referral-amount"[^>]*>\s*([^<]*?)\s*<`)
	reRefSourceTxt   = regexp.MustCompile(`(?s)data-slot="referral-source"[^>]*>(.*?)</td>`)
	reRefDateTitle   = regexp.MustCompile(`data-slot="referral-date"[^>]*\btitle="([^"]*)"`)
	reRefDateText    = regexp.MustCompile(`(?s)data-slot="referral-date"[^>]*>(.*?)</td>`)
	reTag            = regexp.MustCompile(`<[^>]*>`)
)

// FetchResult 是一次抓取解析后的结果。
type FetchResult struct {
	ReportEmail string
	Subscribed  bool
	Rolling     *store.Usage
	Weekly      *store.Usage
	Monthly     *store.Usage
	Referrals   []store.Referral // 邀请奖励列表
	Unclaimed   int              // 未领取（status=available）的邀请奖励数量
}

// Fetch 请求工作空间 Go 页面并解析额度信息。auth 默认作为 Cookie 头，
// 若以 "Bearer " 开头则作为 Authorization 头。
func Fetch(workspaceID, auth string, timeout time.Duration) (*FetchResult, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, fmt.Errorf("工作空间 ID 为空")
	}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf(workspaceURLFmt, workspaceID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if auth = strings.TrimSpace(auth); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			req.Header.Set("Authorization", auth)
		} else {
			req.Header.Set("Cookie", auth)
		}
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d（Auth 可能已失效或 Workspace ID 错误）", resp.StatusCode)
	}

	res := Parse(string(body))
	if res.Rolling == nil && res.Weekly == nil && res.Monthly == nil {
		return nil, fmt.Errorf("未解析到额度数据（Auth 可能已失效、未订阅 Go 或 Workspace ID 错误）")
	}
	return res, nil
}

// Parse 从页面 HTML 中解析额度数据（导出以便测试）。
func Parse(html string) *FetchResult {
	res := &FetchResult{
		Rolling: extractUsage(reRolling, html),
		Weekly:  extractUsage(reWeekly, html),
		Monthly: extractUsage(reMonthly, html),
	}
	if m := reUserEmail.FindStringSubmatch(html); m != nil {
		res.ReportEmail = m[1]
	}
	res.Subscribed = reSubbed.MatchString(html)
	res.Referrals = parseReferrals(html)
	for _, r := range res.Referrals {
		if r.Status == "available" {
			res.Unclaimed++
		}
	}
	return res
}

// parseReferrals 从邀请奖励表中解析每一行（金额 / 来源 / 日期 / 状态 / 方向）。
func parseReferrals(html string) []store.Referral {
	region := html
	if m := reReferralsTable.FindString(html); m != "" {
		region = m
	}
	rows := reReferralRow.FindAllString(region, -1)
	out := make([]store.Referral, 0, len(rows))
	for _, row := range rows {
		r := store.Referral{}
		if m := reRefStatus.FindStringSubmatch(row); m != nil {
			r.Status = m[1]
		}
		if m := reRefSource.FindStringSubmatch(row); m != nil {
			r.Direction = m[1]
		}
		if m := reRefAmount.FindStringSubmatch(row); m != nil {
			r.Amount = strings.TrimSpace(m[1])
		}
		if m := reRefSourceTxt.FindStringSubmatch(row); m != nil {
			r.Source = stripTags(m[1])
		}
		if m := reRefDateTitle.FindStringSubmatch(row); m != nil {
			r.Date = strings.TrimSpace(m[1])
		} else if m := reRefDateText.FindStringSubmatch(row); m != nil {
			r.Date = stripTags(m[1])
		}
		out = append(out, r)
	}
	return out
}

func stripTags(s string) string {
	return strings.TrimSpace(reTag.ReplaceAllString(s, ""))
}

// ClaimReferral 领取（使用）指定的邀请奖励。
//
// opencode 的「使用」按钮是 Next.js Server Action：点击后向工作空间页面 URL
// 发起 POST，携带 `Next-Action: <hash>` 头，参数经 RSC 编码为函数入参；该 hash
// 位于站点前端 JS bundle 中，无法从页面 HTML 推导。待抓取到真实的「使用」网络
// 请求（方法 / URL / 头 / 请求体）后在此实现。
func ClaimReferral(workspaceID, auth string, index int, ref store.Referral, timeout time.Duration) error {
	return fmt.Errorf("领取功能尚未接入：需要 opencode「使用」按钮的网络请求（Next-Action 头与请求体）才能实现")
}

// Expiry 返回作为「到期依据」的额度维度（优先每月，其次每周、滚动）。
func (r *FetchResult) Expiry() *store.Usage {
	if r.Monthly != nil {
		return r.Monthly
	}
	if r.Weekly != nil {
		return r.Weekly
	}
	return r.Rolling
}

func extractUsage(re *regexp.Regexp, html string) *store.Usage {
	m := re.FindStringSubmatch(html)
	if m == nil {
		return nil
	}
	body := m[1]
	u := &store.Usage{}
	if s := reStatus.FindStringSubmatch(body); s != nil {
		u.Status = s[1]
	}
	if s := reReset.FindStringSubmatch(body); s != nil {
		u.ResetInSec, _ = strconv.ParseInt(s[1], 10, 64)
	}
	if s := rePercent.FindStringSubmatch(body); s != nil {
		u.UsagePercent, _ = strconv.Atoi(s[1])
	}
	return u
}
