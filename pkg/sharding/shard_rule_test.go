package sharding

import (
	"strings"
	"testing"
	"time"
)

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
		got, err := r.GetTableNameByTime(ts)
		if err != nil {
			t.Fatalf("%s: GetTableNameByTime error: %v", c.rule, err)
		}
		if got != c.want {
			t.Errorf("%s: got %s, want %s", c.rule, got, c.want)
		}
	}
}

func TestByWeekUsesISOYear(t *testing.T) {
	r := NewShardRule(ByWeek, "logs")
	d := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	weekYear, week := d.ISOWeek()
	if weekYear != 2026 || week != 53 {
		t.Fatalf("前提断言失败：该日 ISO 周应为 2026-W53，实际 %d-W%d", weekYear, week)
	}
	got, err := r.GetTableNameByTime(d)
	if err != nil {
		t.Fatalf("GetTableNameByTime error: %v", err)
	}
	if got != "logs_2026W53" {
		t.Errorf("ByWeek 跨年边界: got %s, want logs_2026W53", got)
	}
}

func TestRangeEnumerationConsistentWithSinglePoint(t *testing.T) {
	loc := time.UTC
	rules := []ShardType{ByYear, ByQuarter, ByMonth, ByWeek, ByTenDay}

	start := time.Date(2025, 11, 15, 0, 0, 0, 0, loc)
	end := time.Date(2026, 3, 5, 0, 0, 0, 0, loc)

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
		for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
			want, err := r.GetTableNameByTime(d)
			if err != nil {
				t.Fatalf("%s: GetTableNameByTime(%s) error: %v", rt, d, err)
			}
			if !set[want] {
				t.Errorf("%s: 单点表名 %s（%s）未被范围枚举覆盖: %v", rt, want, d.Format("2006-01-02"), tables)
				break
			}
		}
	}
}

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

// GetTableNameByTime 对非时间分片类型必须返回 error
func TestGetTableNameByTimeRejectsIDShardTypes(t *testing.T) {
	r := NewShardRule(ByIDHash, "users")
	_, err := r.GetTableNameByTime(time.Now())
	if err == nil {
		t.Error("ByIDHash 调 GetTableNameByTime 应返回 error")
	}

	r2 := NewShardRule(ByIDRange, "orders")
	_, err = r2.GetTableNameByTime(time.Now())
	if err == nil {
		t.Error("ByIDRange 调 GetTableNameByTime 应返回 error")
	}
}

// GetTableNameByID 对时间分片类型必须返回 error
func TestGetTableNameByIDRejectsTimeShardTypes(t *testing.T) {
	r := NewShardRule(ByMonth, "orders")
	_, err := r.GetTableNameByID(12345)
	if err == nil {
		t.Error("ByMonth 调 GetTableNameByID 应返回 error")
	}
}

// ByIDRange 边界一致性：写入路由和范围枚举用完全相同的公式
func TestByIDRangeBoundaryConsistency(t *testing.T) {
	rule := NewShardRule(ByIDRange, "orders", WithRangeSize(1000))

	for _, id := range []int64{0, 999, 1000, 1001, 1999, 2000} {
		writeTable, err := rule.GetTableNameByID(id)
		if err != nil {
			t.Fatalf("GetTableNameByID(%d) error: %v", id, err)
		}
		tables, err := rule.GetTableNamesByIDRange(IDRange{Start: id, End: id})
		if err != nil {
			t.Fatalf("GetTableNamesByIDRange(%d,%d) error: %v", id, id, err)
		}
		if len(tables) != 1 || tables[0] != writeTable {
			t.Errorf("id=%d: 写入路由 %s 与范围枚举 %v 不一致", id, writeTable, tables)
		}
	}
}

func TestIDBasedSharding(t *testing.T) {
	hashRule := NewShardRule(ByIDHash, "users", WithHashShards(8))
	single, err := hashRule.GetTableNamesByIDRange(IDRange{Start: 12345, End: 12345})
	if err != nil || len(single) != 1 {
		t.Fatalf("hash 单 ID 查询应命中 1 表: %v %v", single, err)
	}
	pt, err := hashRule.GetTableNameByID(12345)
	if err != nil {
		t.Fatal(err)
	}
	if single[0] != pt {
		t.Errorf("hash 单点与枚举不一致: %s vs %s", single[0], pt)
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
	if len(tbls) != 3 {
		t.Errorf("范围分片 50~250 应命中 3 表, got %v", tbls)
	}
}