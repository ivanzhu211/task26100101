package adserve

import (
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func window() (time.Time, time.Time) { return t0, t0.Add(time.Hour) }

// 两个 CPM 不同的广告位共用同一活动，余额不足以同时满足两次预占。
func TestSharedBudgetAcrossZones(t *testing.T) {
	clock := &fakeClock{t: t0}
	start, end := window()
	e := New(clock,
		[]CampaignConfig{{
			ID: 1, BudgetCents: 10, Weight: 1, Start: start, End: end,
			ZoneCreatives: map[string][]int64{"z1": {101}, "z2": {102}},
		}},
		map[string]ZoneConfig{"z1": {CPM: 6000}, "z2": {CPM: 5000}},
	)

	cid, ok := e.Decide("z1", "r1")
	if !ok || cid != 101 {
		t.Fatalf("first decide = %d,%v want 101,true", cid, ok)
	}
	if _, ok := e.Decide("z2", "r2"); ok {
		t.Fatal("second decide should fail: 6000+5000 > 10000")
	}

	// 重试同一 requestID：返回首次结果，预算与决策计数不再变化。
	cid, ok = e.Decide("z1", "r1")
	if !ok || cid != 101 {
		t.Fatalf("retry r1 = %d,%v want 101,true", cid, ok)
	}
	if _, ok := e.Decide("z2", "r2"); ok {
		t.Fatal("retry r2 should still be no-ad")
	}

	c := e.campaigns[1]
	if c.reserved != 6000 || c.spent != 0 || c.decisions != 1 {
		t.Fatalf("state = reserved %d spent %d decisions %d, want 6000/0/1",
			c.reserved, c.spent, c.decisions)
	}
}

// 到期前确认成功；到期时刻确认失败；释放后余额可被新决策使用；金额守恒。
func TestConfirmAndExpire(t *testing.T) {
	clock := &fakeClock{t: t0}
	start, end := window()
	e := New(clock,
		[]CampaignConfig{{
			ID: 1, BudgetCents: 15, Weight: 1, Start: start, End: end,
			ZoneCreatives: map[string][]int64{"z": {101}},
		}},
		map[string]ZoneConfig{"z": {CPM: 6000}},
	)
	c := e.campaigns[1]

	// 到期前确认成功。
	if _, ok := e.Decide("z", "r1"); !ok {
		t.Fatal("decide r1 failed")
	}
	clock.Advance(29 * time.Second)
	if !e.Confirm("r1") {
		t.Fatal("confirm r1 before expiry should succeed")
	}
	if c.spent != 6000 || c.reserved != 0 {
		t.Fatalf("after confirm: spent %d reserved %d, want 6000/0", c.spent, c.reserved)
	}
	// 重复确认仍成功且只消费一次。
	if !e.Confirm("r1") || c.spent != 6000 {
		t.Fatal("re-confirm r1 should succeed without double charge")
	}

	// 到期时刻确认失败。
	if _, ok := e.Decide("z", "r2"); !ok {
		t.Fatal("decide r2 failed")
	}
	clock.Advance(30 * time.Second)
	if e.Confirm("r2") {
		t.Fatal("confirm r2 at expiry moment should fail")
	}
	// 释放前预占仍占预算：新决策余额不足。
	if c.reserved != 6000 {
		t.Fatalf("reserved before Expire = %d, want 6000", c.reserved)
	}
	if _, ok := e.Decide("z", "r3"); ok {
		t.Fatal("decide r3 should fail while r2 reservation holds budget")
	}
	// r3 的无广告结果被记忆，重试仍返回无广告。
	if _, ok := e.Decide("z", "r3"); ok {
		t.Fatal("retry r3 should still be no-ad")
	}

	// 释放过期预占后，新决策可使用余额。
	e.Expire()
	if c.reserved != 0 {
		t.Fatalf("reserved after Expire = %d, want 0", c.reserved)
	}
	e.Expire() // 重复清理不得重复释放
	if c.reserved != 0 || c.spent != 6000 {
		t.Fatalf("after double Expire: spent %d reserved %d, want 6000/0", c.spent, c.reserved)
	}
	if e.Confirm("r2") {
		t.Fatal("confirm expired r2 should keep failing")
	}
	if _, ok := e.Decide("z", "r4"); !ok {
		t.Fatal("decide r4 after Expire should succeed")
	}
	if !e.Confirm("r4") {
		t.Fatal("confirm r4 should succeed")
	}

	// 金额守恒：已消费+预占不超过预算，且无重复扣费。
	if c.spent != 12000 || c.reserved != 0 || c.spent+c.reserved > c.budget {
		t.Fatalf("final state spent %d reserved %d budget %d", c.spent, c.reserved, c.budget)
	}
	// 不存在与无广告请求的确认返回失败。
	if e.Confirm("nope") {
		t.Fatal("confirm unknown request should fail")
	}
}

// 重试已到期预占返回首次结果，不重新选号也不复活预占。
func TestRetryExpiredReservation(t *testing.T) {
	clock := &fakeClock{t: t0}
	start, end := window()
	e := New(clock,
		[]CampaignConfig{{
			ID: 1, BudgetCents: 100, Weight: 1, Start: start, End: end,
			ZoneCreatives: map[string][]int64{"z": {101}},
		}},
		map[string]ZoneConfig{"z": {CPM: 6000}},
	)
	c := e.campaigns[1]

	if _, ok := e.Decide("z", "r1"); !ok {
		t.Fatal("decide r1 failed")
	}
	clock.Advance(31 * time.Second)
	e.Expire()
	cid, ok := e.Decide("z", "r1")
	if !ok || cid != 101 {
		t.Fatalf("retry expired r1 = %d,%v want 101,true", cid, ok)
	}
	if c.reserved != 0 || c.decisions != 1 {
		t.Fatalf("retry revived reservation: reserved %d decisions %d", c.reserved, c.decisions)
	}
	if e.Confirm("r1") {
		t.Fatal("confirm expired r1 should fail")
	}
}

// 加权选号：按 (decisions+1)/weight 升序，并列取活动ID较小者。
func TestWeightedSelection(t *testing.T) {
	clock := &fakeClock{t: t0}
	start, end := window()
	e := New(clock,
		[]CampaignConfig{
			{ID: 1, BudgetCents: 1000, Weight: 1, Start: start, End: end,
				ZoneCreatives: map[string][]int64{"z": {101}}},
			{ID: 2, BudgetCents: 1000, Weight: 2, Start: start, End: end,
				ZoneCreatives: map[string][]int64{"z": {102}}},
		},
		map[string]ZoneConfig{"z": {CPM: 1000}},
	)

	want := []int64{102, 101, 102, 102, 101}
	for i, w := range want {
		cid, ok := e.Decide("z", string(rune('a'+i)))
		if !ok || cid != w {
			t.Fatalf("decide %d = %d,%v want %d,true", i, cid, ok, w)
		}
	}
	if e.campaigns[1].decisions != 2 || e.campaigns[2].decisions != 3 {
		t.Fatalf("decisions = %d/%d, want 2/3",
			e.campaigns[1].decisions, e.campaigns[2].decisions)
	}
}

// 投放时间窗只影响新决策。
func TestWindow(t *testing.T) {
	clock := &fakeClock{t: t0}
	e := New(clock,
		[]CampaignConfig{
			{ID: 1, BudgetCents: 100, Weight: 1,
				Start: t0.Add(time.Minute), End: t0.Add(90 * time.Second),
				ZoneCreatives: map[string][]int64{"z": {101}}},
		},
		map[string]ZoneConfig{"z": {CPM: 1000}},
	)

	if _, ok := e.Decide("z", "early"); ok {
		t.Fatal("decide before window should be no-ad")
	}
	clock.Advance(time.Minute)
	if _, ok := e.Decide("z", "in"); !ok {
		t.Fatal("decide at window start should succeed")
	}
	clock.Advance(29 * time.Second)
	// 窗口内确认成功。
	if !e.Confirm("in") {
		t.Fatal("confirm inside window should succeed")
	}
	clock.Advance(time.Second)
	if _, ok := e.Decide("z", "late"); ok {
		t.Fatal("decide at window end should be no-ad")
	}
	// 窗口结束不影响已确认记录的重复确认。
	if !e.Confirm("in") {
		t.Fatal("re-confirm after window end should still succeed")
	}
}
