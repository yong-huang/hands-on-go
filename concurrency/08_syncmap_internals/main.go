// 项目 8：sync.Map 源码级复盘 —— read/dirty/misses 协议
//
// 分步演示（协议为 Go 1.9~1.23 的经典实现；本机 go1.26 的 sync.Map 已换用
// 并发哈希字典树 HashTrieMap，经典协议用这份带观测钩子的复刻来验证）：
//
//	步骤 1  read 命中：无锁快路径，misses 不动
//	步骤 2  miss 累积 → 阈值触发 promote：dirty 升级为 read
//	步骤 3  dirty 反复重建的性能悬崖：每写一个新 key 都要全量复制 read
package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// entry 是值的原子槽位，三种状态：nil=已删除（墓碑）、expunged=已从 dirty 注销、其他=有效值。
// read 里的 entry 指针被读者共享，改值用 CAS/写指针完成——这就是"entry 只读加速"：
// 读者不复制、不加锁，只做一次 map 查找加一次原子读。
type entry struct{ p atomic.Pointer[any] }

var expunged = new(any) // 注销哨兵：read 中存在、dirty 中不存在的标记

func (e *entry) load() (any, bool) {
	v := e.p.Load()
	if v == nil || v == expunged {
		return nil, false
	}
	return *v, true
}

func (e *entry) tryStore(v any) bool { // 读侧无锁更新；遇 expunged 报告失败
	for {
		old := e.p.Load()
		if old == expunged {
			return false
		}
		if e.p.CompareAndSwap(old, &v) {
			return true
		}
	}
}

func (e *entry) storeLocked(v any) { e.p.Store(&v) } // 持 mu 的更新路径

func (e *entry) expungeLocked() bool   { return e.p.CompareAndSwap(nil, expunged) }
func (e *entry) unexpungeLocked() bool { return e.p.CompareAndSwap(expunged, nil) }

// readOnly 是不可变的只读快照：Load 的快路径只碰它，不加锁。
type readOnly struct {
	m       map[string]*entry
	amended bool // true：dirty 里有 read 没有的 key
}

// ProtocolMap 复刻经典 sync.Map 的 read/dirty/misses 协议，并加计数钩子。
type ProtocolMap struct {
	mu     sync.Mutex
	read   atomic.Pointer[readOnly] // 只读快照，atomic.Pointer 发布
	dirty  map[string]*entry        // 持 mu 才能访问的完整视图
	misses int                      // 自上次 promote 以来的 miss 计数

	// 观测钩子：标准库没有，为验证协议流转而加
	Misses     atomic.Int64 // Load 落到 dirty 的次数
	Promotes   atomic.Int64 // dirty 升级为 read 的次数
	DirtyBuilt atomic.Int64 // dirty 从 read 重建的次数
}

func NewProtocolMap() *ProtocolMap {
	pm := &ProtocolMap{}
	pm.read.Store(&readOnly{m: map[string]*entry{}})
	return pm
}

func (m *ProtocolMap) Load(key string) (any, bool) {
	read := m.read.Load()
	e, ok := read.m[key]
	if !ok && read.amended {
		m.mu.Lock() // 锁点 1：read 未命中且已 amended
		read = m.read.Load()
		e, ok = read.m[key] // 双检：别的 goroutine 可能刚 promote
		if !ok && read.amended {
			e, ok = m.dirty[key]
			m.missLocked()
		}
		m.mu.Unlock()
	}
	if !ok {
		return nil, false
	}
	return e.load()
}

// missLocked 在持 mu 下累计 miss；达到 len(dirty) 阈值就把 dirty 整体提升为 read。
func (m *ProtocolMap) missLocked() {
	m.misses++
	m.Misses.Add(1)
	if m.misses < len(m.dirty) {
		return
	}
	m.read.Store(&readOnly{m: m.dirty}) // 锁点 2：promote，dirty 的 map 原地变成 read
	m.dirty = nil
	m.misses = 0
	m.Promotes.Add(1)
}

func (m *ProtocolMap) Store(key string, v any) {
	read := m.read.Load()
	if e, ok := read.m[key]; ok && e.tryStore(v) {
		return // 无锁快路径：read 命中且 entry 未被注销
	}
	m.mu.Lock() // 锁点 3：慢路径全程持 mu
	read = m.read.Load()
	if e, ok := read.m[key]; ok {
		if e.unexpungeLocked() {
			m.dirty[key] = e // 注销态复活：重新登记进 dirty
		}
		e.storeLocked(v)
	} else if e, ok := m.dirty[key]; ok {
		e.storeLocked(v)
	} else {
		if !read.amended { // read 之外的第一个新 key
			m.dirtyLocked() // 从 read 重建 dirty（全量复制 entry 指针）
			m.read.Store(&readOnly{m: read.m, amended: true})
		}
		m.dirty[key] = newEntry(v)
	}
	m.mu.Unlock()
}

// dirtyLocked 把 read 的全部 entry 指针复制进新 dirty；已删除的标记为 expunged 排除掉。
func (m *ProtocolMap) dirtyLocked() {
	if m.dirty != nil {
		return
	}
	read := m.read.Load()
	m.dirty = make(map[string]*entry, len(read.m))
	for k, e := range read.m {
		if !e.expungeLocked() {
			m.dirty[k] = e
		}
	}
	m.DirtyBuilt.Add(1)
}

func newEntry(v any) *entry {
	e := &entry{}
	e.storeLocked(v)
	return e
}

func main() {
	fmt.Println("== 步骤 1：新 key 落 dirty、首读必 miss、promote 后 read 命中零计数 ==")
	m := NewProtocolMap()
	m.Store("gpu", "H100") // read 未 amended：重建 dirty（复制 0 个 entry）并翻开 amended
	fmt.Printf("  首个新 key：DirtyBuilt=%d（诚实预期：1）；此刻 read 为空，key 只在 dirty\n",
		m.DirtyBuilt.Load())
	m.Load("gpu") // 首读：read miss → dirty 命中 → misses=1 == len(dirty)=1 → 立即 promote
	fmt.Printf("  首读 1 次：Misses=%d Promotes=%d（诚实预期：1 1，新 key 必经一次慢路径）\n",
		m.Misses.Load(), m.Promotes.Load())
	for i := 0; i < 1000; i++ {
		m.Load("gpu")
	}
	// 在看什么：promote 后 gpu 在 read 里，1000 次命中零 miss、全程不碰 mu
	fmt.Printf("  再读 1000 次：Misses=%d（诚实预期：仍为 1）——entry 只读加速\n", m.Misses.Load())

	fmt.Println("== 步骤 2：miss 累积到 len(dirty)，触发 promote ==")
	m2 := NewProtocolMap()
	m2.Store("cpu", "M4")
	m2.Store("gpu", "H100")
	m2.Store("npu", "N1")
	fmt.Printf("  写 3 个 key：DirtyBuilt=%d（诚实预期：1，只有首个新 key 重建）；len(dirty)=%d\n",
		m2.DirtyBuilt.Load(), len(m2.dirty))
	m2.Load("missing")
	m2.Load("missing")
	fmt.Printf("  2 次 miss：Misses=%d Promotes=%d（诚实预期：2 0，未达阈值 3）\n",
		m2.Misses.Load(), m2.Promotes.Load())
	m2.Load("missing") // 第 3 次：misses=3 == len(dirty)=3 → promote
	fmt.Printf("  第 3 次 miss：Misses=%d Promotes=%d（诚实预期：3 1）；dirty=nil、amended 翻回 false\n",
		m2.Misses.Load(), m2.Promotes.Load())
	v, _ := m2.Load("cpu")
	fmt.Printf("  promote 后 Load(\"cpu\")=%v——数据完好，此后命中新 read\n", v)

	fmt.Println("== 步骤 3：dirty 反复重建的性能悬崖 ==")
	c := NewProtocolMap()
	for round := 0; round < 5; round++ {
		for i := 0; i < 10; i++ { // 10 个新 key 落进 dirty
			c.Store(fmt.Sprintf("k%d-%d", round, i), i)
		}
		n := len(c.dirty)
		for i := 0; i < n; i++ { // 用 len(dirty) 次 miss 把 dirty 整体 promote 进 read
			c.Load("missing")
		}
		size := len(c.read.Load().m)
		before := c.DirtyBuilt.Load()
		c.Store(fmt.Sprintf("new-%d", round), round) // 再写 1 个新 key → 触发全量重建
		// 在看什么：read 越大，"加一个新 key"要复制的 entry 越多——O(n) 放大
		fmt.Printf("  round %d：promote 后 read 规模=%d，存 1 个新 key 重建 %d 次（累计 %d，复制 %d 个 entry）\n",
			round, size, c.DirtyBuilt.Load()-before, c.DirtyBuilt.Load(), size)
	}
	fmt.Println("  结论：read 攒到 N 条后，每个新 key 都付 O(N) 复制——经典协议的 dirty 重建悬崖；")
	fmt.Println("  高频增删 key 的三方基准（真 sync.Map / RWMutex+map / 分片）：go test -bench=. -benchmem -run=^$")
}
