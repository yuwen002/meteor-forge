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

// SonyflakeIDGenerator 封装了Sonyflake生成器
// 提供了更易用的接口来生成和解析分布式ID
type SonyflakeIDGenerator struct {
	flake     *sonyflake.Sonyflake // Sonyflake生成器实例
	startTime time.Time            // ID生成器的起始时间
	machineID uint16               // 机器ID
}

// IDInfo 包含了解析后的ID详细信息
// 用于调试和监控目的，可以查看ID的各个组成部分
type IDInfo struct {
	ID        uint64
	Timestamp time.Time
	MachineID uint16
	Sequence  uint16
}

// NewSonyflakeIDGenerator 创建并初始化一个新的Sonyflake ID生成器
// 参数:
//   - startTime: ID生成器的起始时间，建议设置为应用的上线时间
//
// 返回值:
//   - *SonyflakeIDGenerator: 初始化后的ID生成器实例
//   - error: 初始化过程中发生的错误
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

// Generate 生成一个新的全局唯一ID
// 返回值:
//   - uint64: 生成的64位ID
//   - error: 生成过程中发生的错误
func (g *SonyflakeIDGenerator) Generate() (uint64, error) {
	return g.flake.NextID()
}

// Parse 解析一个已存在的ID，提取其中的时间戳、机器ID和序列号信息
// 参数:
//   - id: 要解析的64位ID
//
// 返回值:
//   - *IDInfo: 包含了解析后的ID信息
//   - error: 解析过程中发生的错误
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

// GenerateFromTimestamp 使用指定的时间戳生成ID，主要用于数据迁移或历史数据导入
// 参数:
//   - ts: 指定的时间戳
//   - sequence: 序列号
//
// 返回值:
//   - uint64: 生成的ID
//   - error: 如果时间戳早于生成器的起始时间则返回错误
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
	// 1. 环境变量
	if idStr := os.Getenv("MACHINE_ID"); idStr != "" {
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
	initErr             error
)

// getOrInitGenerator 获取或初始化全局生成器
func getOrInitGenerator() (*SonyflakeIDGenerator, error) {
	globalGeneratorOnce.Do(func() {
		globalGenerator, initErr = NewSonyflakeIDGenerator(defaultStartTime)
	})
	return globalGenerator, initErr
}

// InitSonyflake 使用给定的起始时间初始化全局 Sonyflake ID 生成器。
// 并发安全，只会初始化一次。
// 如果已经初始化过，会返回错误。
func InitSonyflake(startTime time.Time) error {
	var err error
	globalGeneratorOnce.Do(func() {
		defaultStartTime = startTime
		globalGenerator, err = NewSonyflakeIDGenerator(startTime)
	})
	if globalGenerator != nil && !defaultStartTime.Equal(startTime) {
		return errors.New("generator already initialized with different start time")
	}
	return err
}

// GenerateID 使用全局 Sonyflake 生成器生成一个新的全局唯一 ID。
// 如果未初始化，会使用默认的起始时间自动初始化。
func GenerateID() (uint64, error) {
	gen, err := getOrInitGenerator()
	if err != nil {
		return 0, err
	}
	return gen.Generate()
}

// ParseID 将 Sonyflake ID 解析为其组成部分（时间戳、机器ID、序列号）。
// 如果未初始化，会使用默认的起始时间自动初始化。
func ParseID(id uint64) (*IDInfo, error) {
	gen, err := getOrInitGenerator()
	if err != nil {
		return nil, err
	}
	return gen.Parse(id)
}

// GenerateIDFromTimestamp 根据指定的时间戳生成 Sonyflake ID。
// 主要用于数据迁移或回填历史数据。
// 如果未初始化，会使用默认的起始时间自动初始化。
func GenerateIDFromTimestamp(ts time.Time, seq uint16) (uint64, error) {
	gen, err := getOrInitGenerator()
	if err != nil {
		return 0, err
	}
	return gen.GenerateFromTimestamp(ts, seq)
}
