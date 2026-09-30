package adengine

import "time"

// Clock 是可注入的单调时钟。
type Clock interface {
	Now() time.Time
}

// CampaignConfig 活动配置，构造时传入且不再变化。
type CampaignConfig struct {
	ID            int64
	BudgetCents   int64
	Weight        int64 // 1..10
	Start         time.Time
	End           time.Time         // 左闭右开 [Start, End)
	ZoneCreatives map[int64][]int64 // 广告位 -> 创意ID列表
}

// ZoneConfig 广告位配置。
type ZoneConfig struct {
	ID  int64
	CPM int64 // 正整数，单位：千分之一分
}

// Decision 一次决策结果。
type Decision struct {
	CreativeID int64
	HasAd      bool
}
