# task26100101 — 进程内广告决策库

处理跨广告位共享预算的加权选号与曝光预占。纯内存实现，无外部依赖。

## 构建与测试

```sh
go build ./...
go test ./...        # 或 go test -race ./...
```

## 使用示例

```go
clk := &myClock{} // 实现 adengine.Clock（单调时钟）
e := adengine.New(clk,
    []adengine.CampaignConfig{{
        ID: 1, BudgetCents: 10, Weight: 1,
        Start: start, End: end, // 左闭右开
        ZoneCreatives: map[int64][]int64{100: {11}, 200: {12}},
    }},
    []adengine.ZoneConfig{{ID: 100, CPM: 6000}, {ID: 200, CPM: 5000}},
)

d := e.Decide(zoneID, requestID) // 选创意并预占；同 requestID 重试返回首次结果
e.Confirm(requestID)             // 预占转已消费；幂等
e.Expire()                       // 释放到期（30s）未确认的预占
```

## 语义要点

- 金额统一为千分之一分：预算 = `BudgetCents * 1000`，单次曝光费用 = 广告位 `CPM`。
- 选号：仅考虑在投放窗内、支持该广告位且 `已消费+预占+本次费用 <= 预算` 的活动；
  按 `(成功决策数+1)/权重` 精确比值（交叉相乘比较）升序，并列取活动ID较小者；计数跨广告位共享。
- 预占有效期 30 秒；`Confirm` 在到期时刻或之后不得首次成功；只有 `Expire` 释放过期预占。
- `Confirm` 幂等：成功后可重复调用，只消费一次；不存在/无广告/已到期未确认的请求返回失败。
