# adserve

进程内广告决策库：跨广告位共享预算的加权选号与曝光预占。配置在构造时传入且不再变化，创意只用 ID 表示。

## 核心语义

- 金额统一为千分之一分：活动预算 = `BudgetCents * 1000`，一次曝光费用 = 广告位的 `CPM`。
- `Decide(zoneID, requestID)`：在投放窗内、支持该广告位且余额足够的活动中，按 `(decisions+1)/weight` 精确比值升序选号（并列取活动 ID 较小者），成功则预占 30 秒曝光费用并增加共享决策计数。相同 `requestID` 的重试永远返回首次结果（含无广告），不重新选号、不复活已到期预占。
- `Confirm(requestID)`：到期前首次确认将预占转为已消费；成功确认可重复调用且只消费一次。不存在、无广告或已到期的请求返回失败。
- `Expire()`：释放已到期且未确认的预占；释放前预占仍占预算；已确认记录不受影响，重复清理不重复释放。

## 使用示例

```go
clock := &myClock{} // 实现 adserve.Clock：Now() time.Time
e := adserve.New(clock,
    []adserve.CampaignConfig{{
        ID: 1, BudgetCents: 100, Weight: 2,
        Start: start, End: end, // 左闭右开投放窗
        ZoneCreatives: map[string][]int64{"banner": {101, 102}},
    }},
    map[string]adserve.ZoneConfig{"banner": {CPM: 500}}, // 500 千分之一分/次
)

creativeID, ok := e.Decide("banner", "req-123") // 选号并预占
if ok {
    // ... 渲染创意，曝光发生后：
    e.Confirm("req-123") // 预占转消费
}
e.Expire() // 周期性调用，释放到期未确认的预占
```

## 运行测试

```sh
go test ./...
```
