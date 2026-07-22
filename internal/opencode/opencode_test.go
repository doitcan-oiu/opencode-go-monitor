package opencode

import (
	"testing"

	"opencode-monitor/internal/store"
)

// 取自真实返回的 <script> 片段
const sample = `
$R[28]($R[1], "zwqn9kx@briarst.us");
$R[28]($R[18], $R[33] = {
    mine: !0,
    region: $R[34] = ["us", "eu", "sg"],
    rollingUsage: $R[35] = { status: "ok", resetInSec: 13157, usagePercent: 1 },
    weeklyUsage: $R[36] = { status: "ok", resetInSec: 599875, usagePercent: 0 },
    monthlyUsage: $R[37] = { status: "ok", resetInSec: 2348518, usagePercent: 50 }
});
您已订阅 OpenCode Go。 liteSubscriptionID: "sub_1Tti5R2StuRr0lbXWnemfM01"
<div data-hk="0000000100000000000100000500a140050021" data-slot="referrals-table"><table data-slot="referrals-table-element"><thead><tr><th>奖励</th><th>描述</th><th>日期</th><th></th></tr></thead><tbody>
<tr data-hk="0000000100000000000100000500a1400500220" data-status="available" data-source="inviter"><td data-slot="referral-amount">$5</td><td data-slot="referral-source">已邀请 w6rwq23cmb@harvinton.us</td><td data-slot="referral-date" title="2026年7月22日">2026年7月22日</td><td data-slot="referral-action"><button type="button">查看奖励</button></td></tr>
<tr data-hk="0000000100000000000100000500a1400500221" data-status="available" data-source="invitee"><td data-slot="referral-amount">$5</td><td data-slot="referral-source">由 zwqn9kx@briarst.us 邀请</td><td data-slot="referral-date" title="2026年7月20日">2026年7月20日</td><td data-slot="referral-action"><button type="button">查看奖励</button></td></tr>
<tr data-hk="0000000100000000000100000500a1400500222" data-status="applied" data-source="inviter"><td data-slot="referral-amount">$5</td><td data-slot="referral-source">已邀请 ty9ke20xv0g2@harvinton.us</td><td data-slot="referral-date" title="2026年7月18日">2026年7月18日</td><td data-slot="referral-action"><button type="button" disabled="">奖励已使用</button></td></tr>
</tbody></table></div>
`

func TestParse(t *testing.T) {
	r := Parse(sample)
	if r.ReportEmail != "zwqn9kx@briarst.us" {
		t.Errorf("email = %q", r.ReportEmail)
	}
	if !r.Subscribed {
		t.Error("应识别为已订阅")
	}
	check := func(name string, u *store.Usage, reset int64, pct int) {
		if u == nil {
			t.Fatalf("%s 未解析到", name)
		}
		if u.ResetInSec != reset || u.UsagePercent != pct || u.Status != "ok" {
			t.Errorf("%s = %+v, 期望 reset=%d pct=%d", name, u, reset, pct)
		}
	}
	check("rolling", r.Rolling, 13157, 1)
	check("weekly", r.Weekly, 599875, 0)
	check("monthly", r.Monthly, 2348518, 50)
	if r.Expiry() != r.Monthly {
		t.Error("到期依据应为每月")
	}
	if r.Unclaimed != 2 {
		t.Errorf("未领取奖励 = %d, 期望 2", r.Unclaimed)
	}
	if len(r.Referrals) != 3 {
		t.Fatalf("邀请奖励行数 = %d, 期望 3", len(r.Referrals))
	}
	r0 := r.Referrals[0]
	if r0.Status != "available" || r0.Direction != "inviter" || r0.Amount != "$5" ||
		r0.Source != "已邀请 w6rwq23cmb@harvinton.us" || r0.Date != "2026年7月22日" {
		t.Errorf("第一条奖励解析错误: %+v", r0)
	}
	if r.Referrals[2].Status != "applied" {
		t.Errorf("第三条应为 applied, 实为 %q", r.Referrals[2].Status)
	}
}

func TestParseMinified(t *testing.T) {
	min := `x,monthlyUsage:$R[37]={status:"ok",resetInSec:2348518,usagePercent:50},y`
	u := extractUsage(reMonthly, min)
	if u == nil || u.ResetInSec != 2348518 || u.UsagePercent != 50 {
		t.Errorf("minified 解析失败: %+v", u)
	}
}
