package proxy

import (
	"sort"
	"sync"

	"opencode-monitor/internal/store"
)

// Balancer 从数据库中挑选「用量最少」的可用 API Key。
type Balancer struct {
	store *store.Store
	mu    sync.Mutex // 串行化「挑选 + 计数」，避免并发下重复选中同一个
}

func NewBalancer(st *store.Store) *Balancer { return &Balancer{store: st} }

// Pick 返回应使用的账号（不排除任何账号）。
func (b *Balancer) Pick() *store.Account { return b.PickExcluding(nil) }

// PickExcluding 返回应使用的账号，并把其转发计数 +1；跳过 exclude 中的账号 ID。
// 无可用 Key 时返回 nil。
//
// 挑选规则：
//  1. 仅考虑配置了 API Key 且不在 exclude 中的账号；
//  2. 优先「健康」账号：状态正常、未到期、用量水位 < 100%；
//  3. 在候选中按 用量水位升序、转发计数升序 排序，取第一个；
//  4. 若无健康账号，则在所有有 Key 的账号里退而求其次（尽力转发）。
func (b *Balancer) PickExcluding(exclude []string) *store.Account {
	b.mu.Lock()
	defer b.mu.Unlock()

	all, err := b.store.All()
	if err != nil {
		return nil
	}
	skip := make(map[string]bool, len(exclude))
	for _, id := range exclude {
		skip[id] = true
	}
	var keyed, healthy []*store.Account
	for _, a := range all {
		if a.APIKey == "" || skip[a.ID] {
			continue
		}
		keyed = append(keyed, a)
		if a.Status == "ok" && !a.Expired() && a.UsageScore() < 100 {
			healthy = append(healthy, a)
		}
	}
	pool := healthy
	if len(pool) == 0 {
		pool = keyed
	}
	if len(pool) == 0 {
		return nil
	}
	sort.SliceStable(pool, func(i, j int) bool {
		si, sj := pool[i].UsageScore(), pool[j].UsageScore()
		if si != sj {
			return si < sj
		}
		return pool[i].ProxyCount < pool[j].ProxyCount
	})
	chosen := pool[0]
	_ = b.store.IncrProxyCount(chosen.ID)
	chosen.ProxyCount++ // 同步内存值，供本次排序后的后续调用参考
	return chosen
}
