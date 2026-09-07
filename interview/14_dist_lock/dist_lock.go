// Distributed Lock —— 分布式锁（内存模拟）
// 面试高频题: 互斥、TTL 自动释放、holder 验证、死锁预防

package main

import (
	"fmt"
	"sync"
	"time"
)

// ── 1. 核心数据结构 ──

// lockEntry 表示一个锁条目
type lockEntry struct {
	holder    string    // 持有者标识
	expiresAt time.Time // 过期时间
}

// DistributedLock 基于 sync.Map 的分布式锁（单机模拟）
type DistributedLock struct {
	locks sync.Map       // key → *lockEntry
	mu    sync.Mutex     // 保护同一个 key 的原子检查-设置
	stop  chan struct{}  // 停止清理协程
}

// NewDistributedLock 创建分布式锁实例，并启动过期清理协程
func NewDistributedLock() *DistributedLock {
	dl := &DistributedLock{stop: make(chan struct{})}
	go dl.cleanup(1 * time.Second)
	return dl
}

// Lock 阻塞式获取锁：如果锁空闲或已过期则获取，否则轮询等待
func (dl *DistributedLock) Lock(key string, holder string, ttl time.Duration) bool {
	for {
		dl.mu.Lock()
		if val, ok := dl.locks.Load(key); ok {
			entry := val.(*lockEntry)
			if time.Now().After(entry.expiresAt) {
				// 已过期，可以抢占
				dl.locks.Store(key, &lockEntry{
					holder:    holder,
					expiresAt: time.Now().Add(ttl),
				})
				dl.mu.Unlock()
				return true
			}
		} else {
			// 锁空闲，直接获取
			dl.locks.Store(key, &lockEntry{
				holder:    holder,
				expiresAt: time.Now().Add(ttl),
			})
			dl.mu.Unlock()
			return true
		}
		dl.mu.Unlock()
		time.Sleep(10 * time.Millisecond) // 轮询间隔
	}
}

// TryLock 非阻塞式获取锁：立即返回，不等待
func (dl *DistributedLock) TryLock(key string, holder string, ttl time.Duration) bool {
	dl.mu.Lock()
	defer dl.mu.Unlock()

	if val, ok := dl.locks.Load(key); ok {
		entry := val.(*lockEntry)
		if !time.Now().After(entry.expiresAt) {
			return false // 锁被他人持有且未过期
		}
	}

	dl.locks.Store(key, &lockEntry{
		holder:    holder,
		expiresAt: time.Now().Add(ttl),
	})
	return true
}

// Unlock 释放锁：仅持有者本人才能释放（防止误释放）
func (dl *DistributedLock) Unlock(key string, holder string) bool {
	dl.mu.Lock()
	defer dl.mu.Unlock()

	val, ok := dl.locks.Load(key)
	if !ok {
		return false // 锁不存在
	}
	entry := val.(*lockEntry)
	if entry.holder != holder {
		return false // 持有者不匹配，拒绝释放
	}
	dl.locks.Delete(key)
	return true
}

// cleanup 定期清理过期锁
func (dl *DistributedLock) cleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			dl.locks.Range(func(key, value any) bool {
				entry := value.(*lockEntry)
				if time.Now().After(entry.expiresAt) {
					dl.locks.Delete(key)
				}
				return true
			})
		case <-dl.stop:
			return
		}
	}
}

// Stop 关闭清理协程
func (dl *DistributedLock) Stop() {
	close(dl.stop)
}

// ── 2. 演示入口 ──

func main() {
	fmt.Println("=== Distributed Lock Demo ===\n")

	dl := NewDistributedLock()
	defer dl.Stop()

	key := "resource:order:1001"

	// ── Demo 1: 基本获取与释放 ──
	fmt.Println("1. Basic Lock/Unlock:")
	if dl.TryLock(key, "service-A", 3*time.Second) {
		fmt.Println("   service-A acquired lock (TTL=3s)")
	}
	if !dl.TryLock(key, "service-B", 3*time.Second) {
		fmt.Println("   service-B TryLock failed (lock held by service-A)")
	}
	if dl.Unlock(key, "service-A") {
		fmt.Println("   service-A released lock")
	}
	if dl.TryLock(key, "service-B", 3*time.Second) {
		fmt.Println("   service-B acquired lock after release")
	}
	dl.Unlock(key, "service-B")

	// ── Demo 2: holder 验证（防误释放）──
	fmt.Println("\n2. Holder Verification:")
	dl.TryLock(key, "service-A", 5*time.Second)
	fmt.Println("   service-A acquired lock")
	if !dl.Unlock(key, "service-B") {
		fmt.Println("   service-B tried to unlock -> DENIED (not holder)")
	}
	if dl.Unlock(key, "service-A") {
		fmt.Println("   service-A unlocked -> OK (is holder)")
	}

	// ── Demo 3: TTL 自动过期 ──
	fmt.Println("\n3. TTL Auto-Expiry:")
	dl.TryLock(key, "service-A", 500*time.Millisecond)
	fmt.Println("   service-A acquired lock (TTL=500ms)")
	time.Sleep(600 * time.Millisecond)
	fmt.Println("   (waited 600ms, lock should be expired)")
	if dl.TryLock(key, "service-B", 3*time.Second) {
		fmt.Println("   service-B acquired lock (auto-expired)")
	}
	dl.Unlock(key, "service-B")

	// ── Demo 4: 多 goroutine 竞争 ──
	fmt.Println("\n4. Goroutine Competition (5 goroutines, same lock):")
	var wg sync.WaitGroup
	results := make(chan string, 10)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			holder := fmt.Sprintf("worker-%d", id)
			if dl.Lock(key, holder, 200*time.Millisecond) {
				fmt.Printf("   %s acquired lock\n", holder)
				time.Sleep(100 * time.Millisecond) // 模拟工作
				dl.Unlock(key, holder)
				results <- fmt.Sprintf("   %s finished work & released", holder)
			}
		}(i)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for r := range results {
		fmt.Println(r)
	}

	// ── Demo 5: TryLock 非阻塞模式 ──
	fmt.Println("\n5. TryLock Non-Blocking Mode:")
	dl.TryLock(key, "owner", 2*time.Second)
	fmt.Println("   owner holds lock")
	success := 0
	for i := 0; i < 3; i++ {
		holder := fmt.Sprintf("try-%d", i)
		if dl.TryLock(key, holder, 1*time.Second) {
			success++
		}
	}
	fmt.Printf("   3 TryLock attempts: %d succeeded, %d failed\n", success, 3-success)
	dl.Unlock(key, "owner")

	fmt.Println("\n=== All demos complete ===")
}
