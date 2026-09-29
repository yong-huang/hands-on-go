# 02 · LFU Cache：频次桶 + 双向链表，Get/Put 均 O(1)

> LFU（Least Frequently Used，最不经常使用）淘汰**访问频率最低**的条目：一个 key 即使很久没被访问，只要历史上被频繁使用就会被保留。读完本篇，你能用「`items` map + `freqList` 频次桶 + `minFreq` 游标」三件套实现 Get/Put 都是常数时间 O(1) 的 LFU 缓存，并从真实演示看清每一轮桶内变化。

## Background

上一代主流做法是 LRU（Least Recently Used，最久未使用，按最近访问时间淘汰的缓存策略）。它只看"最近"，有一个已知盲区：一次对冷数据的批量扫描会把真正的热点全部冲掉，这个现象称为扫描污染——一次性顺序读走大量不会被复用的数据，把它们在"最近使用"维度上全部刷成最新。

具体场景：缓存里存着天天被读取的热点配置，某个批处理任务顺序扫过一大批只读一次的历史 key。扫描结束后，LRU 链表里全是这些冷 key，热点配置反而被挤出缓存，随后的请求只能回源重建。

LFU 换一个维度：按频率淘汰，访问 100 次的热点 key 不会被偶尔路过的 key 挤掉。但频率会变——每次访问都要给条目"搬家"，朴素做法（数组 + 每次遍历找最小频率）是 O(n)。经典 O(1) 方案就是本文的结构：**map 定位 + 频次桶分层 + minFreq 直接定位淘汰桶**。

## What

LFU（Least Frequently Used，最不经常使用）是一种按访问频率淘汰条目的缓存策略：容量满时，淘汰历史上被访问次数最少的条目。作为数据结构，它是"两个 map 加一组双向链表"的组合：`items` 定位条目，`freqList` 按频率分层，`minFreq` 指示当前最低频率。

可以把频次桶想象成一栋只上不下的楼梯，每个台阶是同一频率的条目排成的队伍：条目每被访问一次就上一层；淘汰时从最低的台阶踢走排尾的人。但和真实楼梯不同的是，"上一层"是纯指针摘接——靠条目随身携带的节点指针一步完成，不需要沿队伍找位置，所以也是 O(1)。

| 部件 | 职责 | 关键约定 |
|---|---|---|
| `items map[int]*item` | 按 key 一跳找到条目 | item 里存有节点指针 `elem` |
| `freqList map[int]*list.List` | 按频率分桶 | 每桶一条双向链表：front=最近访问、back=最久访问 |
| `minFreq int` | 游标，指示最低频率 | 淘汰时直接取 `freqList[minFreq].Back()` |

淘汰时直接取最低频桶的链表尾，全程无遍历；条目在桶间只升不降。

## When to Use

这一节给出判断依据：什么负载值得为 LFU 的多层结构付出复杂度，什么情况下 LRU 更合适。

- **缓存会遭遇批量扫描时**：备份、全表导出、批处理任务会顺序读走大量只读一次的冷数据，需要保住长期热点不被冲掉。
- **热点集合长期稳定时**：少数 key 承载绝大部分读请求（如头部商品、全局配置），按频率保留它们比按最近时间更稳。
- **条目价值随累计访问次数上升时**：判断"谁值得留在内存"的依据是历史总量而不是最近一次访问。

何时不用：

- 访问以时间局部性为主（刚访问过的短时间内还会再访问）：LRU 用一个 map 加一条链表就能实现，结构简单得多。
- 新条目占比高、需要被快速服务：LFU 对新条目不友好——新 key 频率为 1，最先被淘汰。
- 数据量小到全部装得下：直接用 map，任何淘汰策略都是多余。

同类方案对比：

| 方案 | 与 O(1) LFU 的差异 | 什么时候选它 |
|---|---|---|
| LRU | 只看"最近"，一个 map + 一条双向链表 | 时间局部性强、批量扫描少 |
| 朴素版 LFU | 同一策略，但每次淘汰要遍历找最小频率，O(n) | 条目数极少、淘汰不频繁 |
| LFU + aging | 频率按时间衰减，缓解新条目劣势 | 热点会缓慢转移的生产环境 |

## Quick Start

前置条件：只需安装 Go 工具链，代码无第三方依赖。运行：

```bash
cd patterns/02_lfu_cache
go run lfu_cache.go
```

预期输出（节选）：

```text
1. Put:
  Put(1,10)
  Put(2,20)
  Put(3,30)
  LFU(cap=3, minFreq=1, size=3):
    freq=1: [3:30] [2:20] [1:10]
2. Get 提升频率:
  Get(1) x2, Get(2) x1
    freq=1: [3:30]
    freq=2: [2:20]
    freq=3: [1:10]
3. 容量满 Put → 淘汰 minFreq 桶尾:
  Put(4,40) → 淘汰 key=3 (freq=1, 最久未访问)
4. 同频率淘汰最久未使用:
  Get(4) 后 Put(5,50) → 淘汰 key=2 (freq=2 桶尾)
  LFU(cap=3, minFreq=1, size=3):
    freq=1: [5:50]
    freq=2: [4:40]
    freq=3: [1:10]
```

`Show()` 从 freq=1 到 10 逐桶打印，桶内顺序就是链表 front→back。步骤 3、4 演示了两种淘汰：跨桶淘汰最低频（key=3）、同频桶内淘汰链表尾（key=2）。

实现围绕两个结构体展开。先看定义，这段在做什么：`item` 随身携带自己在桶链表中的节点指针 `elem`；`LFUCache` 用两个 map 分别按 key 和按频率索引：

```go
type item struct {
    key, value, freq int
    elem *list.Element // 指向所在频率桶链表中的节点
}

type LFUCache struct {
    cap      int
    minFreq  int
    items    map[int]*item       // key → item，O(1) 定位
    freqList map[int]*list.List  // freq → 双向链表（频次桶）
}
```

`container/list` 是标准库提供的双向链表。`item.elem` 是 O(1) 的关键：`Remove` 需要节点指针，把它存在 item 里，移桶时不用遍历链表。

## How It Works

机制的主线是：**条目在频次桶组成的阶梯上只升不降，淘汰时从最低台阶的队尾踢人**。下面按一次 Get 和一次 Put 的顺序展开。

### Get：命中即提频

```go
func (c *LFUCache) Get(key int) (int, bool) {
    it, ok := c.items[key]
    if !ok { return 0, false }
    c.incrementFreq(it) // 频率+1，搬到更高频桶
    return it.value, true
}
```

查找交给 `items` map，剩下的工作全在 `incrementFreq` 里。对照演示输出：`Get(1) x2, Get(2) x1` 后，key=1 从 freq=1 一路升到 freq=3，key=2 升到 freq=2，freq=1 桶只剩 `[3:30]`。

### incrementFreq：O(1) 移桶

```go
// 频率 +1：从旧桶移到新桶
func (c *LFUCache) incrementFreq(it *item) {
    oldFreq := it.freq
    l := c.freqList[oldFreq]
    l.Remove(it.elem)
    if l.Len() == 0 {
        delete(c.freqList, oldFreq)
        if c.minFreq == oldFreq {
            c.minFreq++
        }
    }
    it.freq++
    if c.freqList[it.freq] == nil {
        c.freqList[it.freq] = list.New()
    }
    it.elem = c.freqList[it.freq].PushFront(it)
}
```

移桶三步：靠 `elem` 指针从旧桶 O(1) 摘出；旧桶搬空则删桶，仅当空的是最低频桶时才把 `minFreq` 加一；头插进新桶并把 `elem` 指向新节点。任何一步退化成遍历（比如找最小频率），整体复杂度就不再是 O(1)。

### Put：更新走提频，新插入走淘汰

```go
func (c *LFUCache) Put(key, value int) {
    if c.cap == 0 {
        return
    }
    if it, ok := c.items[key]; ok {
        it.value = value
        c.incrementFreq(it)
        return
    }
    // 容量满，淘汰 minFreq 桶的最久未使用（链表尾部）
    if len(c.items) >= c.cap {
        l := c.freqList[c.minFreq]
        back := l.Back()
        evict := back.Value.(*item)
        l.Remove(back)
        delete(c.items, evict.key)
    }
    // 插入新 item，freq=1
    it := &item{key: key, value: value, freq: 1}
    if c.freqList[1] == nil {
        c.freqList[1] = list.New()
    }
    it.elem = c.freqList[1].PushFront(it)
    c.items[key] = it
    c.minFreq = 1
}
```

输出步骤 3：`Put(4,40)` 时容量满，淘汰 minFreq=1 桶尾 key=3。步骤 4：`Get(4)` 把 4 提到 freq=2 桶并 `PushFront`，桶内变成 front `[4:40]` → back `[2:20]`；随后 `Put(5,50)` 淘汰的正是这个 back——key=2。

注意此时 minFreq 已随 freq=1 桶搬空推进到 2，`Show()` 打印的 `minFreq=1` 是因为步骤 4 又插入了新条目 5（新插入必置 1）。

### 关键字段一览

| 字段/操作 | 含义 | 为什么重要 |
|---|---|---|
| `items map[int]*item` | key → 条目 | Get/Put 第一步 O(1) 定位 |
| `freqList map[int]*list.List` | freq → 频次桶链表 | 同频内嵌 LRU：front 最近、back 最久 |
| `minFreq int` | 当前最低频率 | 淘汰时直接定位桶，免 O(n) 扫描 |
| `item.elem *list.Element` | 所在链表节点指针 | `Remove(elem)` O(1) 移桶的前提 |
| `freqList[minFreq].Back()` | 淘汰对象 | 最低频 + 同频最久未使用 |

`back.Value.(*item)` 是类型断言（从 `any` 接口值取回具体类型）：链表节点的 `Value` 字段类型是 `any`，取出时要还原成 `*item` 才能读到 key。

## Pitfalls & Q&A

以下条目都来自手写实现时的真实失误，每条按现象、原因、解法展开。

### 踩坑清单

- **`elem` 指针忘随每次 `PushFront` 更新**。现象：条目搬过一次桶后，下一次 `Remove` 直接 panic 或删错节点。原因：item 在桶间搬家后旧 elem 已作废，指针还指着旧链表。解法：每次 `PushFront` 的返回值立刻写回 `it.elem`。
- **旧桶搬空后未清理**。现象：`freqList` 里残留空链表，`minFreq` 停在旧位置不再前进。原因：只做了 `Remove`，没有检查 `l.Len() == 0`。解法：桶空即 `delete(freqList, oldFreq)`，且仅当 `minFreq == oldFreq` 时才 `minFreq++`——别的桶空了不用动。
- **Put 已存在的 key 被当成"新插入"**。现象：更新一个旧 key 也触发了淘汰，或把 minFreq 重置为 1。原因：走错了分支。解法：已存在时只更新 value 并提频，不触发淘汰、不重置 minFreq。
- **`cap=0` 未直接返回**。现象：构造容量为 0 的缓存后第一次操作就崩溃。原因：`freqList[minFreq]` 在空 map 上取到 nil。解法：入口处对 `cap <= 0` 直接短路返回。

### 深入问答

**Q：Get/Put 的 O(1) 具体由哪几步保证？**
A：见上文 How It Works——map 定位条目、`elem` 指针 O(1) 摘除、`minFreq` 直接定位淘汰桶，三个 O(1) 拼起来。

**Q：LFU 和 LRU 怎么选？**
A：LRU 一个 map + 一条双向链表，时间局部性强时够用；LFU 多一层频次桶和 minFreq 游标，用于对抗扫描污染、保热点。

LFU 的两个缺陷要心里有数：对新条目不友好（新 key freq=1 最先被淘汰），且历史频率不衰减——工业实现常加 aging（按时间衰减频率）缓解，或用 LFU+LRU 混合。

**Q：minFreq 什么时候更新？为什么不会失准？**
A：只有两处：新插入时置 1（新条目频率必为 1）；最低频桶被搬空时 +1。它不会偏高——只要 minFreq 桶非空，桶里就一定还有 freq=minFreq 的条目；也不会偏低——提频只升不降。

**Q：同频率的条目淘汰谁？**
A：同一条链表内 front=最近访问、back=最久访问，淘汰取 back——即同频下退化为 LRU。LFU 本质是"频率优先、频率相同比新旧"的两级比较。

**Q：换掉 `container/list` 自己手写链表要注意什么？**
A：踩坑清单里的三条失误在本文实现中已被规避，换实现时要重新自查一遍；核心是保证等价的 O(1) 摘除能力——`Remove` 必须靠节点自带的前后指针完成，而不是按值遍历查找。
