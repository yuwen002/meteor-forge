// Package sharding 提供了分布式ID生成和分片功能
// 本文件实现了基于Sonyflake算法的分布式ID生成器，支持高并发、分布式环境下的ID生成
package sharding

import (
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/sony/sonyflake" // Sonyflake是一个分布式唯一ID生成器
)

// SonyflakeIDGenerator 封装了 Sonyflake 分布式 ID 生成器。
//
// ID 结构：39 位时间戳（10ms 精度，约 17 年）| 16 位机器 ID | 8 位序列号。
//
// 机器 ID 解析优先级：
//  1. 环境变量 SONYFLAKE_MACHINE_ID（显式指定，推荐容器化部署时使用）
//  2. Pod IP 的后两个字节（Kubernetes 环境自动分配）
//  3. Hostname 哈希取模（兜底方案）
//
// 使用示例：
//
//	gen, _ := sharding.NewSonyflakeIDGenerator(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
//	id, _ := gen.Generate()
//	info, _ := gen.Parse(id)
//	fmt.Println(info.Timestamp) // ID 生成的精确时间（10ms 精度）
type SonyflakeIDGenerator struct {
	flake     *sonyflake.Sonyflake
	startTime time.Time
	machineID uint16
}

// IDInfo 是 Parse 返回的解析结果，包含 ID 的三个位段信息。
// 可用于调试、监控，或基于 ID 的时间/机器维度路由判断。
type IDInfo struct {
	ID        uint64    // 原始 ID
	Timestamp time.Time // ID 生成时的时间戳（基于生成器 startTime 还原）
	MachineID uint16    // 机器 ID（16 位）
	Sequence  uint16    // 序列号（8 位）
}

// NewSonyflakeIDGenerator 创建并初始化一个 Sonyflake ID 生成器。
// 同一集群内所有节点必须使用相同的 startTime，否则 Parse 还原出的时间会错位。
//
// 参数：
//   - startTime: ID 起始时间，建议设为应用正式上线日，从此刻起算 39 位时间戳可覆盖约 17 年。
//
// 返回值：
//   - *SonyflakeIDGenerator: 生成器实例
//   - error: 机器 ID 解析失败或底层 sonyflake 初始化失败时返回
//
// 示例：
//
//	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
//	gen, err := sharding.NewSonyflakeIDGenerator(start)
//	if err != nil {
//	    log.Fatal("初始化 ID 生成器失败:", err)
//	}
func NewSonyflakeIDGenerator(startTime time.Time) (*SonyflakeIDGenerator, error) {
	machineID, err := resolveMachineID()
	if err != nil {
		return nil, err
	}

	settings := sonyflake.Settings{
		StartTime: startTime,
		MachineID: func() (uint16, error) {
			return machineID, nil
		},
		CheckMachineID: func(uint16) bool { return true },
	}

	sf := sonyflake.NewSonyflake(settings)
	if sf == nil {
		return nil, errors.New("failed to initialize sonyflake")
	}

	return &SonyflakeIDGenerator{
		flake:     sf,
		startTime: startTime,
		machineID: machineID,
	}, nil
}

// Generate 生成一个新的全局唯一 ID。线程安全，内部由 sonyflake 的原子计数器保证序列递增。
//
// 示例：
//
//	id, err := gen.Generate()
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Printf("生成 ID: %d\n", id)
func (g *SonyflakeIDGenerator) Generate() (uint64, error) {
	return g.flake.NextID()
}

// Parse 解析一个已有的 Sonyflake ID，拆出时间戳、机器 ID、序列号。
// 逆向路由场景特别有用：拿到一个 order ID，就能知道它属于哪台机器、何时生成。
//
// 参数：
//   - id: 64 位 Sonyflake ID（由 Generate 或 GenerateFromTimestamp 产生）
//
// 返回值：
//   - *IDInfo: 解析结果
//
// 示例：
//
//	info, _ := gen.Parse(189472839472)
//	fmt.Println("生成时间:", info.Timestamp)
//	fmt.Println("机器 ID:", info.MachineID)
//	fmt.Println("序列号:", info.Sequence)
func (g *SonyflakeIDGenerator) Parse(id uint64) (*IDInfo, error) {
	// Sonyflake ID 结构: 39位时间戳 | 16位机器ID | 8位序列号
	timestamp := (id >> 24) & 0x1FFFFFFFFFF // 39位时间戳
	t := g.startTime.Add(time.Duration(timestamp) * 10 * time.Millisecond)
	machineID, sequence := parseMachineAndSequence(id)
	return &IDInfo{
		ID:        id,
		Timestamp: t,
		MachineID: machineID,
		Sequence:  sequence,
	}, nil
}

// GenerateFromTimestamp 使用指定时间戳和序列号合成一个 Sonyflake ID。
// 主要用途：历史数据迁移/回填场景下，让旧数据也拥有符合当前 ID 规则的主键，
// 从而能正确路由到时间分片表。
//
// 注意：同一 (ts, sequence, machineID) 组合会产生相同 ID，调用方需确保唯一性。
//
// 参数：
//   - ts: 指定的时间戳，必须 >= 生成器 startTime 且在 39 位时间戳可表示范围内
//   - sequence: 序列号，低 8 位有效（256 个值）
//
// 返回值：
//   - uint64: 合成的 Sonyflake ID
//   - error: ts 早于 startTime 或超出 39 位时间范围时返回
//
// 示例：
//
//	// 为 2024-06-15 的历史订单生成 ID，确保它们路由到 orders_202406 分片表
//	historicalID, _ := gen.GenerateFromTimestamp(
//	    time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC), 42,
//	)
//	rule.GetTableNameByTime(time.Date(2024, 6, 15, ...)) // → orders_202406
func (g *SonyflakeIDGenerator) GenerateFromTimestamp(ts time.Time, sequence uint16) (uint64, error) {
	if ts.Before(g.startTime) {
		return 0, errors.New("timestamp before startTime")
	}

	// 时间差 / 10ms
	elapsed := uint64(ts.Sub(g.startTime).Milliseconds() / 10)
	if elapsed >= (1 << 39) {
		return 0, errors.New("timestamp too large for sonyflake")
	}

	// 组成 ID（Sonyflake 布局：机器ID占 16 位移至 bit8，序列号占低 8 位）
	id := (elapsed << 24) | (uint64(g.machineID) << 8) | uint64(sequence&0xFF)
	return id, nil
}

// --------------------- 工具函数 --------------------
// 以下是一些内部使用的辅助函数

func resolveMachineID() (uint16, error) {
	// 1. 环境变量 SONYFLAKE_MACHINE_ID
	if idStr := os.Getenv("SONYFLAKE_MACHINE_ID"); idStr != "" {
		if id, err := strconv.ParseUint(idStr, 10, 16); err == nil {
			return uint16(id), nil
		}
	}

	// 2. Pod IP
	if ipID, err := machineIDFromPodIP(); err == nil {
		return ipID, nil
	}

	// 3. Hostname hash
	if hID, err := machineIDFromHostname(); err == nil {
		return hID, nil
	}

	return 0, errors.New("cannot determine machine ID")
}

// machineIDFromPodIP 从Pod的IP地址生成机器ID
// 在Kubernetes等容器环境中，使用Pod IP可以确保每个容器有唯一的机器ID
func machineIDFromPodIP() (uint16, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return 0, err
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			ip := ipnet.IP.To4()
			if ip != nil {
				return uint16(ip[2])<<8 | uint16(ip[3]), nil
			}
		}
	}
	return 0, errors.New("no pod ip found")
}

// machineIDFromHostname 从主机名生成机器ID
// 当无法获取Pod IP时，使用主机名作为备选方案
func machineIDFromHostname() (uint16, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return 0, err
	}
	var hash uint32
	for _, c := range hostname {
		hash = hash*31 + uint32(c)
	}
	return uint16(hash % 1024), nil
}

// parseMachineAndSequence 从ID中解析出机器ID和序列号
// 参数:
//   - id: 64位ID
//
// 返回值:
//   - uint16: 机器ID（16 位）
//   - uint16: 序列号（8 位）
func parseMachineAndSequence(id uint64) (uint16, uint16) {
	machineID := uint16((id >> 8) & 0xFFFF)
	sequence := uint16(id & 0xFF)
	return machineID, sequence
}

// --------------------- 全局实例 --------------------
// 提供全局单例模式的ID生成器，方便在应用中使用
// 默认的起始时间（可以在编译时通过 -ldflags 覆盖）
var defaultStartTime = time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)

var (
	globalGenerator     *SonyflakeIDGenerator
	globalGeneratorOnce sync.Once
	initOnce            sync.Once
	initStartTime       time.Time
	initCalled          bool
	initErr             error
)

// getOrInitGenerator 获取或初始化全局生成器
func getOrInitGenerator() (*SonyflakeIDGenerator, error) {
	globalGeneratorOnce.Do(func() {
		startTime := defaultStartTime
		if initCalled {
			startTime = initStartTime
		}
		globalGenerator, initErr = NewSonyflakeIDGenerator(startTime)
	})
	return globalGenerator, initErr
}

// InitSonyflake 初始化全局 Sonyflake ID 生成器（包级单例）。
// 并发安全，只会生效一次。若已被其他代码以不同 startTime 初始化，将返回 error。
//
// 建议在应用启动最早阶段调用一次，避免并发首次调用时因机器 ID 解析竞争导致行为不可预测。
//
// 示例：
//
//	func init() {
//	    if err := sharding.InitSonyflake(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
//	        panic(err)
//	    }
//	}
func InitSonyflake(startTime time.Time) error {
	initOnce.Do(func() {
		initCalled = true
		initStartTime = startTime
	})
	if !initStartTime.Equal(startTime) {
		return errors.New("generator already initialized with different start time")
	}
	return nil
}

// GenerateID 使用全局单例生成器生成一个唯一 ID。
// 如果全局生成器尚未初始化，会使用默认 startTime (2023-01-01 UTC) 自动初始化。
//
// 示例：
//
//	id, err := sharding.GenerateID()
//	_ = id // 直接使用
func GenerateID() (uint64, error) {
	gen, err := getOrInitGenerator()
	if err != nil {
		return 0, err
	}
	return gen.Generate()
}

// ParseID 解析一个 Sonyflake ID，返回其组成部分。
// 内部使用全局生成器的 startTime 还原时间戳；若全局生成器尚未初始化，会自动以默认值初始化。
//
// 示例：
//
//	info, err := sharding.ParseID(189472839472)
//	fmt.Println("创建于:", info.Timestamp)
func ParseID(id uint64) (*IDInfo, error) {
	gen, err := getOrInitGenerator()
	if err != nil {
		return nil, err
	}
	return gen.Parse(id)
}

// GenerateIDFromTimestamp 使用全局生成器按指定时间戳合成 Sonyflake ID。
// 历史数据迁移场景的便捷函数。
//
// 示例：
//
//	id, _ := sharding.GenerateIDFromTimestamp(
//	    time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC), 0,
//	)
func GenerateIDFromTimestamp(ts time.Time, seq uint16) (uint64, error) {
	gen, err := getOrInitGenerator()
	if err != nil {
		return 0, err
	}
	return gen.GenerateFromTimestamp(ts, seq)
}