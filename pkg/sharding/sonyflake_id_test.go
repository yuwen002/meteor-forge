package sharding

import (
	"sync"
	"testing"
	"time"
)

// 生成器创建 + ID 生成/解析往返
func TestSonyflakeGenerateAndParse(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	g, err := NewSonyflakeIDGenerator(start)
	if err != nil {
		t.Fatalf("create generator: %v", err)
	}

	id1, err := g.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	id2, err := g.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if id1 == 0 || id2 == 0 || id1 == id2 {
		t.Fatalf("IDs must be non-zero and unique: %d vs %d", id1, id2)
	}

	info, err := g.Parse(id1)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.ID != id1 {
		t.Errorf("parsed ID mismatch: %d vs %d", info.ID, id1)
	}
	// 解析出的时间应落在起始时间与当前时间之间
	now := time.Now()
	if info.Timestamp.Before(start) || info.Timestamp.After(now.Add(time.Second)) {
		t.Errorf("parsed timestamp %v out of range [%v, %v]", info.Timestamp, start, now)
	}

	// 位段还原：机器ID（16 位）与序列号（8 位）必须与 ID 位段一致
	idBack := (uint64((info.Timestamp.UnixMilli()-start.UnixMilli())/10) << 24) |
		(uint64(info.MachineID) << 8) | uint64(info.Sequence)
	if idBack != id1 {
		t.Errorf("bit-level round trip failed: rebuilt %d != original %d", idBack, id1)
	}
}

// 迁移/回填场景：按指定时间戳构造 ID，解析应还原出该时间点（10ms 精度内）
func TestGenerateFromTimestampRoundTrip(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	g, err := NewSonyflakeIDGenerator(start)
	if err != nil {
		t.Fatalf("create generator: %v", err)
	}

	target := start.Add(72 * time.Hour)
	id, err := g.GenerateFromTimestamp(target, 42)
	if err != nil {
		t.Fatalf("generate from timestamp: %v", err)
	}
	info, err := g.Parse(id)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if diff := info.Timestamp.Sub(target); diff > 10*time.Millisecond || diff < -10*time.Millisecond {
		t.Errorf("timestamp not restored: got %v, want ≈%v", info.Timestamp, target)
	}
	if info.Sequence != 42 {
		t.Errorf("sequence not restored: got %d, want 42", info.Sequence)
	}

	// 早于起始时间的请求必须被拒绝
	if _, err := g.GenerateFromTimestamp(start.Add(-time.Minute), 0); err == nil {
		t.Error("expected error for timestamp before startTime")
	}
}

// 高并发唯一性冒烟测试：2000 个 goroutine 并发生成，结果不得重复
func TestConcurrentGenerateUniqueness(t *testing.T) {
	g, err := NewSonyflakeIDGenerator(time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("create generator: %v", err)
	}

	const n = 2000
	ids := make([]uint64, n)
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			id, err := g.Generate()
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}
			ids[j] = id
		}(i)
	}
	wg.Wait()

	select {
	case err := <-errCh:
		t.Fatalf("generate error under concurrency: %v", err)
	default:
	}

	seen := make(map[uint64]bool, n)
	for i, id := range ids {
		if id == 0 {
			t.Fatalf("id at %d not generated", i)
		}
		if seen[id] {
			t.Fatalf("duplicate id generated: %d", id)
		}
		seen[id] = true
	}
}
