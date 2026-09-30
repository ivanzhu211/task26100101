// Package adserve 提供进程内广告决策：跨广告位共享预算的加权选号与曝光预占。
package adserve

import (
	"sync"
	"time"
)

// reservationTTL 是预占有效期。
const reservationTTL = 30 * time.Second

// Clock 是可注入的单调时钟。
type Clock interface {
	Now() time.Time
}

// CampaignConfig 描述一个广告活动。
type CampaignConfig struct {
	ID          int64
	BudgetCents int64
	Weight      int
	Start       time.Time
	End         time.Time
	// ZoneCreatives 广告位到创意ID列表的映射。
	ZoneCreatives map[string][]int64
}

// ZoneConfig 描述一个广告位。
type ZoneConfig struct {
	// CPM 单次曝光费用，单位为千分之一分。
	CPM int64
}

// Engine 是广告决策引擎。配置在构造时传入且不再变化。
type Engine struct {
	mu          sync.Mutex
	clock       Clock
	campaigns   map[int64]*campaign
	campaignIDs []int64
	zones       map[string]ZoneConfig
	records     map[string]*record
}

// campaign 是活动的运行时状态。
type campaign struct {
	cfg       CampaignConfig
	budget    int64 // 千分之一分
	spent     int64 // 已消费
	reserved  int64 // 预占中
	decisions int64 // 累计成功决策数
}

// record 是一次 Decide 的持久结果。
type record struct {
	noAd       bool
	campaignID int64
	creativeID int64
	cost       int64
	expiresAt  time.Time
	confirmed  bool
	expired    bool
}

// New 构造引擎。
func New(clock Clock, campaigns []CampaignConfig, zones map[string]ZoneConfig) *Engine {
	e := &Engine{
		clock:     clock,
		campaigns: make(map[int64]*campaign, len(campaigns)),
		zones:     zones,
		records:   make(map[string]*record),
	}
	for _, cfg := range campaigns {
		e.campaigns[cfg.ID] = &campaign{cfg: cfg, budget: cfg.BudgetCents * 1000}
		e.campaignIDs = append(e.campaignIDs, cfg.ID)
	}
	return e
}

// Decide 为广告位选择创意并预占一次曝光费用。
// 相同 requestID 的重试返回首次结果。
func (e *Engine) Decide(zoneID, requestID string) (creativeID int64, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if rec, exists := e.records[requestID]; exists {
		if rec.noAd {
			return 0, false
		}
		return rec.creativeID, true
	}

	zone, zoneOK := e.zones[zoneID]
	now := e.clock.Now()
	if !zoneOK {
		e.records[requestID] = &record{noAd: true}
		return 0, false
	}

	var chosen *campaign
	for _, id := range e.campaignIDs {
		c := e.campaigns[id]
		if now.Before(c.cfg.Start) || !now.Before(c.cfg.End) {
			continue
		}
		if len(c.cfg.ZoneCreatives[zoneID]) == 0 {
			continue
		}
		if c.spent+c.reserved+zone.CPM > c.budget {
			continue
		}
		if chosen == nil || less(c, chosen) {
			chosen = c
		}
	}
	if chosen == nil {
		e.records[requestID] = &record{noAd: true}
		return 0, false
	}

	creativeID = chosen.cfg.ZoneCreatives[zoneID][0]
	e.records[requestID] = &record{
		campaignID: chosen.cfg.ID,
		creativeID: creativeID,
		cost:       zone.CPM,
		expiresAt:  now.Add(reservationTTL),
	}
	chosen.reserved += zone.CPM
	chosen.decisions++
	return creativeID, true
}

// less 按 (decisions+1)/weight 的精确比值升序比较，并列取活动ID较小者。
func less(a, b *campaign) bool {
	lhs := (a.decisions + 1) * int64(b.cfg.Weight)
	rhs := (b.decisions + 1) * int64(a.cfg.Weight)
	if lhs != rhs {
		return lhs < rhs
	}
	return a.cfg.ID < b.cfg.ID
}

// Confirm 确认曝光，将预占转为已消费。
func (e *Engine) Confirm(requestID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	rec, exists := e.records[requestID]
	if !exists || rec.noAd {
		return false
	}
	if rec.confirmed {
		return true
	}
	if rec.expired || !e.clock.Now().Before(rec.expiresAt) {
		return false
	}
	c := e.campaigns[rec.campaignID]
	c.reserved -= rec.cost
	c.spent += rec.cost
	rec.confirmed = true
	return true
}

// Expire 释放已到期且未确认的预占。
func (e *Engine) Expire() {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.clock.Now()
	for _, rec := range e.records {
		if rec.noAd || rec.confirmed || rec.expired {
			continue
		}
		if now.Before(rec.expiresAt) {
			continue
		}
		e.campaigns[rec.campaignID].reserved -= rec.cost
		rec.expired = true
	}
}
