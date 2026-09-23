# 02 · LFU Cache：频次桶 + 双向链表，Get/Put 均 O(1)

> LFU（Least Frequently Used）淘汰**访问频率最低**的条目：即使一个 key 很久没被访问，只要它历史上被频繁使用，LFU 就会保留它——这是 LRU 做不到的。难点在于：既要按频率分层，又要 O(1) 完成查找、提频与淘汰。本实验用「`items` map + `freqList` 频次桶 + `minFreq` 游标」三件套实现，并跑真实演示看每一轮桶内变化。

## 1. 为什么需要它

缓存容量有限，满了必须淘汰。LRU 只看"最近"，会被一次冷数据的批量扫描把真正的热点全部冲掉（扫描污染）；LFU 看"频率"，访问 100 次的热点 key 不会被偶尔路过的 key 挤掉。挑战是频率会变：每次访问都要给条目"搬家"，朴素做法（数组 + 遍历找最小频率）是 O(n)。LFU 的经典 O(1) 方案就是本实验的结构：**map 定位 + 频次桶分层 + minFreq 直接定位淘汰桶**。

## 2. 总览：核心机制一图看懂

![LFU Cache 架构：items map + freqList 频次桶 + minFreq 淘汰](images/lfu.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/patterns/02_lfu_cache/images/lfu.html)
> （或本地打开 [`images/lfu.html`](images/lfu.html)）。

**心智模型一句话**：`items` map 负责 O(1) 找到条目，`freqList` 把条目按访问频率分桶（每桶一条双向链表，front=最近访问、back=最久访问），`minFreq` 指向最低频桶——淘汰时直接取 `freqList[minFreq].Back()`，全程无遍历。图中桶间的"freq+1 移桶"边就是 Get 命中后条目的升桶路径（图中自左向右升频）。

## 3. 快速开始

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

## 4. 核心概念

### 4.1 三件套数据结构

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

`item.elem` 是 O(1) 的关键：`container/list` 的 `Remove` 需要节点指针，把它存在 item 里，移桶时不用遍历链表。

### 4.2 Get：命中即提频

```go
func (c *LFUCache) Get(key int) (int, bool) {
    it, ok := c.items[key]
    if !ok { return 0, false }
    c.incrementFreq(it) // 频率+1，搬到更高频桶
    return it.value, true
}
```

### 4.3 incrementFreq：O(1) 移桶

```go
func (c *LFUCache) incrementFreq(it *item) {
    oldFreq := it.freq
    l := c.freqList[oldFreq]
    l.Remove(it.elem)          // 从旧桶摘出（elem 指针 O(1)）
    if l.Len() == 0 {          // 旧桶空了
        delete(c.freqList, oldFreq)
        if c.minFreq == oldFreq {
            c.minFreq++        // 只有这时才推进 minFreq
        }
    }
    it.freq++
    it.elem = c.freqList[it.freq].PushFront(it) // 新桶头部 = 最近访问
}
```

对照演示输出：`Get(1)x2, Get(2)x1` 后，key=1 从 freq=1 一路升到 freq=3，key=2 升到 freq=2，freq=1 桶只剩 `[3:30]`。

### 4.4 Put：更新走提频，新插入走淘汰

```go
func (c *LFUCache) Put(key, value int) {
    if it, ok := c.items[key]; ok {  // 已存在：只改 value + 提频
        it.value = value
        c.incrementFreq(it)
        return
    }
    if len(c.items) >= c.cap {       // 容量满：淘汰 minFreq 桶的链表尾
        l := c.freqList[c.minFreq]
        back := l.Back()
        delete(c.items, back.Value.(*item).key)
        l.Remove(back)
    }
    it := &item{key: key, value: value, freq: 1} // 新节点入 freq=1 桶
    it.elem = c.freqList[1].PushFront(it)
    c.items[key] = it
    c.minFreq = 1
}
```

步骤 3：`Put(4,40)` 时容量满，淘汰 minFreq=1 桶尾 key=3。步骤 4：`Get(4)` 把 4 提到 freq=2 桶并 `PushFront`，桶内变成 front `[4:40]` → back `[2:20]`；随后 `Put(5,50)` 淘汰的正是这个 back——key=2。注意此时 minFreq 已随 freq=1 桶搬空推进到 2，`Show()` 打印的 `minFreq=1` 是因为步骤 4 又插入了新条目 5（新插入必置 1）。

## 5. 关键字段与坑清单

| 字段/操作 | 含义 | 为什么重要 |
|---|---|---|
| `items map[int]*item` | key → 条目 | Get/Put 第一步 O(1) 定位 |
| `freqList map[int]*list.List` | freq → 频次桶链表 | 同频内嵌 LRU：front 最近、back 最久 |
| `minFreq int` | 当前最低频率 | 淘汰时直接定位桶，免 O(n) 扫描 |
| `item.elem *list.Element` | 所在链表节点指针 | `Remove(elem)` O(1) 移桶的前提 |
| `freqList[minFreq].Back()` | 淘汰对象 | 最低频 + 同频最久未使用 |

坑清单：

- **`elem` 指针必须随每次 `PushFront` 更新**——item 在桶间搬家后旧 elem 作废，忘更新下次 `Remove` 直接 panic 或删错节点
- **旧桶搬空要 `delete(freqList, oldFreq)`**，否则残留空链表；且仅当 `minFreq == oldFreq` 时才 `minFreq++`，别的桶空了不用动
- **Put 已存在的 key 不是"新插入"**：只更新 value 并提频，不触发淘汰、不重置 minFreq=1
- **`cap=0` 要直接返回**，否则 `freqList[minFreq]` 在空 map 上取到 nil
- minFreq 的维护只有两处：新插入置 1、最低频桶被搬空时 +1——写对这两处，整体才是 O(1)

## 6. 文件结构

```text
patterns/02_lfu_cache/
├── README.md                   # 本文件：原理 + 运行指引 + 深入要点
├── lfu_cache.go                # 主教材：三件套实现 + 四步淘汰演示
└── images/
    ├── lfu.architecture.json   # 图源：频次桶架构（Typed JSON IR）
    ├── lfu.html                # 交互版：可缩放/高亮连线的自包含页面
    └── lfu.svg                 # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：LFU 的 Get/Put 为什么能做到 O(1)？**
A：三个 O(1) 拼起来——map 定位条目、`elem` 指针从旧链表 O(1) 摘除、`minFreq` 直接定位淘汰桶取 `Back()`。任何一步退化成遍历（比如找最小频率）整体就崩。

**Q：LFU 和 LRU 怎么选？**
A：LRU 一个 map + 一条双向链表，时间局部性强时好；LFU 多一层频次桶和 minFreq 游标，对抗扫描污染、保热点。LFU 的缺陷是对新条目不友好（新 key freq=1 最先被淘汰）且历史频率不衰减——工业实现常加 "aging" 按时间衰减频率，或用 LFU+LRU 混合。

**Q：minFreq 什么时候更新？为什么不会失准？**
A：只有两处：新插入时置 1（新条目频率必为 1）；最低频桶被搬空时 +1。它不会偏高——只要 minFreq 桶非空，桶里就一定还有 freq=minFreq 的条目；也不会偏低——提频只升不降。

**Q：同频率的条目淘汰谁？**
A：同一条链表内 front=最近访问、back=最久访问，淘汰取 back——即同频下退化为 LRU。LFU 本质是"频率优先、频率相同比新旧"的两级比较。

**Q：手写这道题最容易错在哪？**
A：三个高频翻车点：① `PushFront` 后忘更新 `item.elem`；② 旧桶搬空后忘了清理 `freqList` 和推进 `minFreq`；③ 把"更新已有 key"当成新插入，错误触发淘汰或把 minFreq 重置为 1。另外用 `container/list` 时 `Remove` 依赖 elem 指针，换实现（如手写链表）要保证等价的 O(1) 摘除能力。

## 8. 总结

LFU 的全部 O(1) 都来自"空间换定位"：map 换查找、elem 指针换摘除、minFreq 换淘汰定位。记住心智模型——**条目在频次桶组成的阶梯上只升不降，淘汰时从最低台阶的队尾踢人**。跑一遍 `go run lfu_cache.go`，对照四步输出看桶内变化，比背模板可靠得多。

下一篇 [03_read4k](../03_read4k/) 看一道 IO 经典题：为什么按 4096 字节分块读取，以及分块管线怎么搭。
