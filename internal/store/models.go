package store

import "time"

// Usage 是单个额度维度（滚动 / 每周 / 每月）的用量信息。
type Usage struct {
	Status       string `json:"status"`
	ResetInSec   int64  `json:"resetInSec"`
	UsagePercent int    `json:"usagePercent"`
}

// Referral 是一条邀请奖励记录。
type Referral struct {
	Amount    string `json:"amount"`    // 金额，如 $5
	Source    string `json:"source"`    // 描述，如「已邀请 x@y.us」/「由 x@y.us 邀请」
	Date      string `json:"date"`      // 日期文案
	Status    string `json:"status"`    // available（可领取）| applied（已使用）
	Direction string `json:"direction"` // inviter | invitee
}

// Account 是一个被监控的 opencode 账号。
type Account struct {
	ID          string `json:"id"`
	Account     string `json:"account"`
	Password    string `json:"password"`
	AuxEmail    string `json:"auxEmail"`
	WorkspaceID string `json:"workspaceID"`
	Auth        string `json:"auth"`   // 监控用：网页 Cookie 头
	APIKey      string `json:"apiKey"` // 转发用：opencode Go API Key

	Status      string     `json:"status"` // ok / error / pending
	Error       string     `json:"error"`
	ReportEmail string     `json:"reportEmail"`
	Subscribed  bool       `json:"subscribed"`
	Rolling     *Usage     `json:"rolling"`
	Weekly      *Usage     `json:"weekly"`
	Monthly     *Usage     `json:"monthly"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	LastChecked *time.Time `json:"lastChecked"`
	ProxyCount  int64      `json:"proxyCount"`       // 作为转发上游被选中的累计次数
	Unclaimed   int        `json:"unclaimedRewards"` // 未领取的邀请奖励数量
	Referrals   []Referral `json:"referrals"`        // 邀请奖励明细
	CreatedAt   time.Time  `json:"createdAt"`
}

// UsageScore 返回账号的「用量水位」——三档用量中的最大百分比（越高越接近限额）。
// 无数据时返回 0。
func (a *Account) UsageScore() int {
	m := 0
	for _, u := range []*Usage{a.Rolling, a.Weekly, a.Monthly} {
		if u != nil && u.UsagePercent > m {
			m = u.UsagePercent
		}
	}
	return m
}

// Expired 报告账号是否已过（每月重置）到期时间。
func (a *Account) Expired() bool {
	return a.ExpiresAt != nil && time.Now().After(*a.ExpiresAt)
}

// AccountView 是返回给前端的视图。密码 / Auth / APIKey 明文原样返回（供账号信息查看）。
type AccountView struct {
	Account
	HasAuth   bool  `json:"hasAuth"`
	HasKey    bool  `json:"hasKey"`
	ExpiresIn int64 `json:"expiresIn"`
}

func (a *Account) View() AccountView {
	v := AccountView{Account: *a}
	v.HasAuth = a.Auth != ""
	v.HasKey = a.APIKey != ""
	if a.ExpiresAt != nil {
		v.ExpiresIn = int64(time.Until(*a.ExpiresAt).Seconds())
	}
	return v
}

// Model 是一个可转发的 opencode Go 模型。
type Model struct {
	ID       string `json:"id"`       // 模型 ID，如 grok-4.5
	Name     string `json:"name"`     // 展示名
	Protocol string `json:"protocol"` // openai | anthropic
	Endpoint string `json:"endpoint"` // 上游端点（仅供展示/参考，转发按路径镜像）
}

const (
	epOpenAI    = "https://opencode.ai/zen/go/v1/chat/completions"
	epAnthropic = "https://opencode.ai/zen/go/v1/messages"
)

// DefaultModels 是首次运行时写入的模型清单。后续可在设置页增删。
func DefaultModels() []Model {
	oa := func(id, name string) Model { return Model{id, name, "openai", epOpenAI} }
	an := func(id, name string) Model { return Model{id, name, "anthropic", epAnthropic} }
	return []Model{
		oa("grok-4.5", "Grok 4.5"),
		oa("glm-5.2", "GLM-5.2"),
		oa("glm-5.1", "GLM-5.1"),
		oa("kimi-k3", "Kimi K3"),
		oa("kimi-k2.7-code", "Kimi K2.7 Code"),
		oa("kimi-k2.6", "Kimi K2.6"),
		oa("deepseek-v4-pro", "DeepSeek V4 Pro"),
		oa("deepseek-v4-flash", "DeepSeek V4 Flash"),
		oa("mimo-v2.5", "MiMo-V2.5"),
		oa("mimo-v2.5-pro", "MiMo-V2.5-Pro"),
		an("minimax-m3", "MiniMax M3"),
		an("minimax-m2.7", "MiniMax M2.7"),
		an("minimax-m2.5", "MiniMax M2.5"),
		an("qwen3.7-max", "Qwen3.7 Max"),
		an("qwen3.7-plus", "Qwen3.7 Plus"),
		an("qwen3.6-plus", "Qwen3.6 Plus"),
	}
}

// Settings 是可在设置界面调整的业务配置。
type Settings struct {
	RefreshInterval string `json:"refreshInterval"` // 自动刷新间隔，如 "30m"
	Concurrency     int    `json:"concurrency"`     // 并发抓取数
	RequestTimeout  string `json:"requestTimeout"`  // 单次监控请求超时，如 "25s"
	WarnPercent     int    `json:"warnPercent"`     // 达到此用量% 视为「额度紧张」
	ExpirySoonDays  int    `json:"expirySoonDays"`  // 距到期少于此天数视为「即将到期」
	ProxyKey        string `json:"proxyKey"`        // 转发接口鉴权 Key（空=不校验）
	MaxRetries      int    `json:"maxRetries"`      // 转发失败时最多改用其它 Key 重试的次数
}

// DefaultSettings 是首次运行时写入数据库的默认值。
func DefaultSettings() Settings {
	return Settings{
		RefreshInterval: "30m",
		Concurrency:     6,
		RequestTimeout:  "25s",
		WarnPercent:     80,
		ExpirySoonDays:  3,
		ProxyKey:        "",
		MaxRetries:      2,
	}
}
