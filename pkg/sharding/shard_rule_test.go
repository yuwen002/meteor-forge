package sharding

import (
	"strings"
	"testing"
	"time"
)

// 各时间策略的表名格式基准（同时锁定大写 Q/W 的一致性修复）
func TestGetTableNameByTimeFormats(t *testing.T) {
	ts := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		rule ShardType
		want string
	}{
		{ByYear, "orders_2026"},
		{ByQuarter, "orders_2026Q4"},
		{ByMonth, "orders_202611"},
		{ByTenDay, "orders_202611_1"},
	}
	for _, c := range cases {
		r := NewShardRule(c.rule, "orders")
		if got := r.GetTableNameByTime(ts); got != c.want {
			t.Errorf("%s: got %s, want %s", c.rule, got, c.want)
		}
	}
}

// ByWeek 的年份前缀必须与 ISOWeek 返回值同源：
// 2027-01-01（周五）属于 ISO 2026-W53，旧实现用日历年会错写成 2027W53
func TestByWeekUsesISOYear(t *testing.T) {
	r := NewShardRule(ByWeek, "logs")
	d := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	weekYear, week := d.ISOWeek()
	if weekYear != 2026 || week != 53 {
		t.Fatalf("前提断言失败：该日 ISO 周应为 2026-W53，实际 %d-W%d", weekYear, week)
	}
	if got := r.GetTableNameByTime(d); got != "logs_2026W53" {
		t.Errorf("ByWeek 跨年边界: got %s, want logs_2026W53", got)
	}
}

// 核心回归：时间范围枚举出的表名，必须与单点 GetTableNameByTime 的命名完全一致
// （曾发生范围枚举用小写 q/w、单点用大写 Q/W 导致查询漏表）
func TestRangeEnumerationConsistentWithSinglePoint(t *testing.T) {
	loc := time.UTC
	rules := []ShardType{ByYear, ByQuarter, ByMonth, ByWeek, ByTenDay}

	start := time.Date(2025, 11, 15, 0, 0, 0, 0, loc)
	end := time.Date(2026, 3, 5, 0, 0, 0, 0, loc) // 覆盖跨年 + 平年 2 月

	for _, rt := range rules {
		r := NewShardRule(rt, "t")
		tables, err := r.GetTableNamesByTimeRange(TimeRange{Start: start, End: end})
		if err != nil {
			t.Fatalf("%s: range query failed: %v", rt, err)
		}
		set := map[string]bool{}
		for _, tb := range tables {
			set[tb] = true
		}
		// 区间内每个时间点对应的表名都必须出现在枚举结果中
		for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
			if want := r.GetTableNameByTime(d); !set[want] {
				t.Errorf("%s: 单点表名 %s（%s）未被范围枚举覆盖: %v", rt, want, d.Format("2006-01-02"), tables)
				break
			}
		}
	}
}

// 核心回归：平年 2 月的 21~28 日属于第三段，范围枚举不得漏掉 *_3 表
func TestTenDayRangeIncludesFebThirdSegment(t *testing.T) {
	r := NewShardRule(ByTenDay, "log")
	tables, err := r.GetTableNamesByTimeRange(TimeRange{
		Start: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tb := range tables {
		if strings.HasSuffix(tb, "_3") {
			found = true
		}
	}
	if !found {
		t.Errorf("平年 2 月应枚举出第三段表（21~28），got: %v", tables)
	}
}

// ID 哈希 / ID 范围分片：单 ID 定位与范围枚举的基本语义
func TestIDBasedSharding(t *testing.T) {
	hashRule := NewShardRule(ByIDHash, "users", WithHashShards(8))
	single, err := hashRule.GetTableNamesByIDRange(IDRange{Start: 12345, End: 12345})
	if err != nil || len(single) != 1 {
		t.Fatalf("hash 单 ID 查询应命中 1 表: %v %v", single, err)
	}
	if single[0] != hashRule.GetTableNameByID(12345) {
		t.Errorf("hash 单点与枚举不一致: %s vs %s", single[0], hashRule.GetTableNameByID(12345))
	}
	all, _ := hashRule.GetTableNamesByIDRange(IDRange{Start: 1, End: 1_000_000})
	if len(all) != 8 {
		t.Errorf("大范围哈希查询应返回全部分片 8 张, got %d", len(all))
	}

	rangeRule := NewShardRule(ByIDRange, "orders", WithRangeSize(100))
	tbls, err := rangeRule.GetTableNamesByIDRange(IDRange{Start: 50, End: 250})
	if err != nil {
		t.Fatal(err)
	}
	if len(tbls) != 3 { // 落在第 0/1/2 号分片
		t.Errorf("范围分片 50~250 应命中 3 表, got %v", tbls)
	}
}
