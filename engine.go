package adengine

import (
	"sync"
	"time"
)

// reservationTTL 预占有效期。
const reservationTTL = 30 * time.Second

// milliPerCent 千分之一分换算系数。
const milliPerCent = 1000

type campaignState struct {
	cfg       CampaignConfig
	budget    int64 // 千分之一分
	spent     int64 // 已消费，千分之一分
	reserved  int64 // 预占中，千分之一分
	decisions int64 // 累计成功决策数，跨广告位共享
}

type requestRecord struct {
	decision   Decision
	campaignID int64
	cost       int64 // 千分之一分
	reservedAt time.Time
	confirmed  bool
	expired    bool
}

// Engine 进程内广告决策引擎。
type Engine struct {
	mu        sync.Mutex
	clock     Clock
	campaigns []*campaignState // 按活动ID升序
	byID      map[int64]*campaignState
	zones     map[int64]ZoneConfig
	requests  map[int64]*requestRecord
}

// New 构造引擎，配置在构造时传入且不再变化。
func New(clock Clock, campaigns []CampaignConfig, zones []ZoneConfig) *Engine {
	e := &Engine{
		clock:    clock,
		byID:     make(map[int64]*campaignState, len(campaigns)),
		zones:    make(map[int64]ZoneConfig, len(zones)),
		requests: make(map[int64]*requestRecord),
	}
	for _, cfg := range campaigns {
		cs := &campaignState{cfg: cfg, budget: cfg.BudgetCents * milliPerCent}
		e.campaigns = append(e.campaigns, cs)
		e.byID[cfg.ID] = cs
	}
	// 按活动ID升序，便于并列时取较小ID。
	for i := 1; i < len(e.campaigns); i++ {
		for j := i; j > 0 && e.campaigns[j-1].cfg.ID > e.campaigns[j].cfg.ID; j-- {
			e.campaigns[j-1], e.campaigns[j] = e.campaigns[j], e.campaigns[j-1]
		}
	}
	for _, z := range zones {
		e.zones[z.ID] = z
	}
	return e
}

// Decide 选择创意并预占一次曝光费用。
// 相同 requestID 的重试直接返回首次结果，不再选号。
func (e *Engine) Decide(zoneID, requestID int64) Decision {
	e.mu.Lock()
	defer e.mu.Unlock()

	if rec, ok := e.requests[requestID]; ok {
		return rec.decision
	}

	now := e.clock.Now()
	rec := &requestRecord{decision: Decision{HasAd: false}}
	e.requests[requestID] = rec

	zone, ok := e.zones[zoneID]
	if !ok {
		return rec.decision
	}
	cost := zone.CPM // 一次曝光费用等于CPM数值（千分之一分）

	best := e.pickLocked(now, zoneID, cost)
	if best == nil {
		return rec.decision
	}

	creatives := best.cfg.ZoneCreatives[zoneID]
	rec.decision = Decision{CreativeID: creatives[0], HasAd: true}
	rec.campaignID = best.cfg.ID
	rec.cost = cost
	rec.reservedAt = now
	best.reserved += cost
	best.decisions++
	return rec.decision
}

// pickLocked 按 (decisions+1)/weight 精确比值升序选活动，并列取ID较小者。
func (e *Engine) pickLocked(now time.Time, zoneID, cost int64) *campaignState {
	var best *campaignState
	for _, cs := range e.campaigns {
		if now.Before(cs.cfg.Start) || !now.Before(cs.cfg.End) {
			continue
		}
		creatives := cs.cfg.ZoneCreatives[zoneID]
		if len(creatives) == 0 {
			continue
		}
		if cs.spent+cs.reserved+cost > cs.budget {
			continue
		}
		if best == nil || lessRatio(cs, best) {
			best = cs
		}
	}
	return best
}

// lessRatio 比较 (a.decisions+1)/a.weight < (b.decisions+1)/b.weight，
// 用交叉相乘避免浮点误差；campaigns 已按ID升序，严格小于才替换，天然实现并列取小ID。
func lessRatio(a, b *campaignState) bool {
	lhs := (a.decisions + 1) * b.cfg.Weight
	rhs := (b.decisions + 1) * a.cfg.Weight
	return lhs < rhs
}

// Confirm 确认曝光，将预占转为已消费。
// 成功确认可重复调用且只消费一次；不存在、无广告或已到期未确认的请求返回失败。
func (e *Engine) Confirm(requestID int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	rec, ok := e.requests[requestID]
	if !ok || !rec.decision.HasAd {
		return false
	}
	if rec.confirmed {
		return true
	}
	if rec.expired || !e.clock.Now().Before(rec.reservedAt.Add(reservationTTL)) {
		return false
	}
	cs := e.byID[rec.campaignID]
	cs.reserved -= rec.cost
	cs.spent += rec.cost
	rec.confirmed = true
	return true
}

// Expire 释放已到期且未确认的预占。已确认记录不受影响，重复清理不重复释放。
func (e *Engine) Expire() {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.clock.Now()
	for _, rec := range e.requests {
		if !rec.decision.HasAd || rec.confirmed || rec.expired {
			continue
		}
		if now.Before(rec.reservedAt.Add(reservationTTL)) {
			continue
		}
		cs := e.byID[rec.campaignID]
		cs.reserved -= rec.cost
		rec.expired = true
	}
}

// Stats 返回活动当前状态快照，主要用于测试与观测。
func (e *Engine) Stats(campaignID int64) (spent, reserved, decisions int64, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cs, ok := e.byID[campaignID]
	if !ok {
		return 0, 0, 0, false
	}
	return cs.spent, cs.reserved, cs.decisions, true
}
