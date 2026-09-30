package adengine

import (
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newEngine(clk Clock, campaigns []CampaignConfig, zones []ZoneConfig) *Engine {
	return New(clk, campaigns, zones)
}

// 两个CPM不同的广告位共享同一活动预算，余额不足以同时满足两次预占；
// 重试同一requestID，预算与决策计数只变化一次。
func TestSharedBudgetAcrossZonesAndRetry(t *testing.T) {
	clk := &fakeClock{now: t0}
	camp := CampaignConfig{
		ID: 1, BudgetCents: 10, Weight: 1,
		Start: t0.Add(-time.Hour), End: t0.Add(time.Hour),
		ZoneCreatives: map[int64][]int64{100: {11}, 200: {12}},
	}
	zones := []ZoneConfig{{ID: 100, CPM: 6000}, {ID: 200, CPM: 5000}}
	e := newEngine(clk, []CampaignConfig{camp}, zones)

	d1 := e.Decide(100, 1001)
	if !d1.HasAd || d1.CreativeID != 11 {
		t.Fatalf("first decide = %+v, want creative 11", d1)
	}
	spent, reserved, decisions, _ := e.Stats(1)
	if spent != 0 || reserved != 6000 || decisions != 1 {
		t.Fatalf("stats = (%d,%d,%d), want (0,6000,1)", spent, reserved, decisions)
	}

	// 剩余 4000 < 5000，第二次预占失败。
	d2 := e.Decide(200, 1002)
	if d2.HasAd {
		t.Fatalf("second decide should be no-ad, got %+v", d2)
	}

	// 重试两个 requestID：返回首次结果，状态不变。
	if d := e.Decide(100, 1001); d != d1 {
		t.Fatalf("retry r1 = %+v, want %+v", d, d1)
	}
	if d := e.Decide(200, 1002); d.HasAd {
		t.Fatalf("retry r2 should stay no-ad, got %+v", d)
	}
	spent, reserved, decisions, _ = e.Stats(1)
	if spent != 0 || reserved != 6000 || decisions != 1 {
		t.Fatalf("after retry stats = (%d,%d,%d), want (0,6000,1)", spent, reserved, decisions)
	}
}

// 到期前确认成功；到期时刻确认失败；释放过期预占后用新决策使用余额；
// 重复确认与重复清理保持金额守恒。
func TestConfirmExpireAndConservation(t *testing.T) {
	clk := &fakeClock{now: t0}
	camp := CampaignConfig{
		ID: 1, BudgetCents: 10, Weight: 1,
		Start: t0.Add(-time.Hour), End: t0.Add(time.Hour),
		ZoneCreatives: map[int64][]int64{100: {11}},
	}
	zones := []ZoneConfig{{ID: 100, CPM: 4000}}
	e := newEngine(clk, []CampaignConfig{camp}, zones)

	// r1: 到期前确认成功。
	if d := e.Decide(100, 1); !d.HasAd {
		t.Fatal("r1 decide failed")
	}
	clk.Advance(29 * time.Second)
	if !e.Confirm(1) {
		t.Fatal("confirm before expiry should succeed")
	}
	spent, reserved, _, _ := e.Stats(1)
	if spent != 4000 || reserved != 0 {
		t.Fatalf("after confirm stats = (%d,%d), want (4000,0)", spent, reserved)
	}

	// r2: 恰好在到期时刻确认，必须失败。
	if d := e.Decide(100, 2); !d.HasAd {
		t.Fatal("r2 decide failed")
	}
	clk.Advance(30 * time.Second)
	if e.Confirm(2) {
		t.Fatal("confirm at expiry time should fail")
	}
	// 释放前预占仍占预算：余额 10000-4000-4000=2000 < 4000，新决策无广告。
	if d := e.Decide(100, 3); d.HasAd {
		t.Fatal("decide before expire should be no-ad")
	}

	e.Expire()
	spent, reserved, decisions, _ := e.Stats(1)
	if spent != 4000 || reserved != 0 || decisions != 2 {
		t.Fatalf("after expire stats = (%d,%d,%d), want (4000,0,2)", spent, reserved, decisions)
	}
	// 到期后 Confirm 仍失败。
	if e.Confirm(2) {
		t.Fatal("confirm after expire should fail")
	}

	// 释放后用新决策使用余额。
	if d := e.Decide(100, 4); !d.HasAd {
		t.Fatal("decide after expire should succeed")
	}
	if !e.Confirm(4) {
		t.Fatal("confirm r4 should succeed")
	}
	// 重复确认：仍成功，金额不变。
	if !e.Confirm(4) {
		t.Fatal("repeat confirm should succeed")
	}
	// 重复清理：不得重复释放。
	e.Expire()
	e.Expire()
	spent, reserved, decisions, _ = e.Stats(1)
	if spent != 8000 || reserved != 0 || decisions != 3 {
		t.Fatalf("final stats = (%d,%d,%d), want (8000,0,3)", spent, reserved, decisions)
	}
	// 金额守恒：已消费+预占 <= 预算，且已确认记录不被 Expire 释放。
	if spent+reserved > 10000 {
		t.Fatalf("conservation violated: spent+reserved=%d > budget", spent+reserved)
	}

	// 不存在与无广告请求的 Confirm 返回失败。
	if e.Confirm(999) {
		t.Fatal("confirm unknown request should fail")
	}
	if d := e.Decide(100, 5); d.HasAd { // 余额 2000 < 4000
		t.Fatal("r5 should be no-ad")
	}
	if e.Confirm(5) {
		t.Fatal("confirm no-ad request should fail")
	}
}

// 加权选号：按 (decisions+1)/weight 升序，并列取较小活动ID。
func TestWeightedSelection(t *testing.T) {
	clk := &fakeClock{now: t0}
	campaigns := []CampaignConfig{
		{ID: 1, BudgetCents: 100, Weight: 1, Start: t0.Add(-time.Hour), End: t0.Add(time.Hour),
			ZoneCreatives: map[int64][]int64{100: {11}}},
		{ID: 2, BudgetCents: 100, Weight: 2, Start: t0.Add(-time.Hour), End: t0.Add(time.Hour),
			ZoneCreatives: map[int64][]int64{100: {21}}},
	}
	zones := []ZoneConfig{{ID: 100, CPM: 1000}}
	e := newEngine(clk, campaigns, zones)

	// 比值序列：c2(1/2) -> 并列(c1=1/1, c2=2/2)取小ID c1 -> c2(2/2<2/1)。
	wantCreatives := []int64{21, 11, 21}
	for i, want := range wantCreatives {
		d := e.Decide(100, int64(100+i))
		if !d.HasAd || d.CreativeID != want {
			t.Fatalf("decide %d = %+v, want creative %d", i, d, want)
		}
	}
	_, _, d1, _ := e.Stats(1)
	_, _, d2, _ := e.Stats(2)
	if d1 != 1 || d2 != 2 {
		t.Fatalf("decisions = (%d,%d), want (1,2)", d1, d2)
	}
}

// 投放时间窗只影响新决策：窗口外无广告；已存在的预占仍可确认。
func TestTimeWindow(t *testing.T) {
	clk := &fakeClock{now: t0}
	camp := CampaignConfig{
		ID: 1, BudgetCents: 100, Weight: 1,
		Start: t0, End: t0.Add(10 * time.Second),
		ZoneCreatives: map[int64][]int64{100: {11}},
	}
	zones := []ZoneConfig{{ID: 100, CPM: 1000}}
	e := newEngine(clk, []CampaignConfig{camp}, zones)

	// 窗口开始前无广告。
	clk.now = t0.Add(-time.Second)
	if d := e.Decide(100, 1); d.HasAd {
		t.Fatal("before window should be no-ad")
	}
	// 窗口内（左闭）。
	clk.now = t0
	if d := e.Decide(100, 2); !d.HasAd {
		t.Fatal("at window start should serve")
	}
	// 窗口结束（右开）后新决策无广告，但未到期预占仍可确认。
	clk.now = t0.Add(10 * time.Second)
	if d := e.Decide(100, 3); d.HasAd {
		t.Fatal("at window end should be no-ad")
	}
	if !e.Confirm(2) {
		t.Fatal("confirm existing reservation after window should succeed")
	}
}
