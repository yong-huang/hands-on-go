# 04 · Trie 前缀树：children map[rune] + isEnd，一条查询路径

> Trie（发音 "try"，前缀树/字典树）是一种按字符逐级分流的树形结构，核心优势是**共享公共前缀**，让前缀搜索、自动补全的耗时只与字符串长度相关、与词典大小无关。读完本篇，你能用 Go 实现完整的 Insert / Search / StartsWith / Autocomplete，并看清"一条查询路径"如何在树上展开。

## Background

哈希表（Go 里的 map，按键直接定位值的结构）是存字符串词典的默认选择：放进去、查出来都快。但输入 `app`，要给出 `apple / application / apply` 时它帮不上忙——哈希表只能精确命中，任何前缀查询都必须遍历所有 key。

输入联想场景把这个痛点放大：输入框每敲一个字符就要一次前缀查询，遍历全部 key 的开销随词典线性增长，敲得越快、词典越大，代价越高。

Trie 的思路是把"按字符逐级分流"变成结构本身：公共前缀只存一份，前缀操作（StartsWith、Autocomplete、最长前缀匹配）全部只走一条从根到叶的路径，不再与词典总量打交道。

## What

Trie（前缀树/字典树）是一种按字符串逐字符分层的多叉树（每个节点可有任意多个子节点的树形结构）：从根到某节点的路径拼出一个前缀，节点上的 `isEnd` 标记该前缀是否是一个完整单词。

可以把 Trie 想象成一本按字符逐级分册的字典：查 `app` 先翻 `a` 分册，再翻 `ap`，再翻 `app`，一路翻下去路径唯一。

但和纸质字典不同的是，Trie 的"分册"只在插入过的字符上存在——没插入过的字符直接缺席；且公共前缀在树里只存一份，被 `apple / apply / application` 共同使用。

| 部件 | 职责 | 关键约定 |
|---|---|---|
| `children map[rune]*TrieNode` | 字符进 map 分流 | rune（Go 中表示一个 Unicode 字符的类型）作 key |
| `isEnd bool` | 区分"只是前缀"与"完整单词" | `app` 与 `appl` 的行为差异全靠它 |
| `root` | 不存字符的哨兵（占位节点） | 统一处理空前缀 `""` |

## When to Use

这一节给出判断依据：前缀操作是否是负载的主要部分，决定了 Trie 是否值得引入。

- **做输入联想/自动补全时**：搜索引擎、IDE 的输入框每敲一个字符触发一次前缀查询，需要按前缀收集候选词。
- **拼写检查/词表校验时**：判断一个词是否在词表中，同时天然获得"是否有词以这个前缀开头"的判断。
- **做 IP 路由表时**：路由器为转发目的地址做最长前缀匹配——在所有匹配的表项里选前缀最长的那个，这是逐字符路径结构的天然场景。

何时不用：

- 只需要精确查找：map 平均 O(1)（常数时间）优于 Trie 的 O(L)（L 为字符串长度），直接用 map。
- 词典很小且前缀查询很少：遍历一遍也就几行代码，不需要为它建树。

同类方案对比：

| 方案 | 与 Trie 的差异 | 什么时候选它 |
|---|---|---|
| `map[string]T` | 精确查找快；前缀操作只能遍历全部 key | 只按完整 key 存取 |
| `[26]*TrieNode` 数组版 Trie | 无哈希开销更快，但绑死字符集 | 确定的 26 个小写字母场景 |
| 布隆过滤器 | 只能回答"可能存在/一定不存在"，不存数据 | 海量 key 的存在性预筛，见 [05_bloom_filter](../05_bloom_filter/) |

## Quick Start

前置条件：只需安装 Go 工具链，代码无第三方依赖。运行：

```bash
cd patterns/04_trie
go run trie.go
```

真实输出（节选，与 trie.go 逐一对应）：

```text
1. Insert:
  Insert("apple") ... Insert("banana") ...   # 共 8 个单词
2. Search:
  Search("app") = true
  Search("appl") = false
  Search("banana") = true
3. StartsWith:
  StartsWith("ap") = true
  StartsWith("bat") = false
4. Autocomplete:
  Autocomplete("app", 5) = [app, apple, application, apply]
  Autocomplete("ap", 5) = [app, apple, application, apply, apt]
  Autocomplete("c", 5) = []
5. Autocomplete (k=2):
  Autocomplete("app", 2) = [app, apple]
6. Autocomplete (empty prefix, k=3):
  Autocomplete("", 3) = [app, apple, application]
```

注意 `Search("appl") = false` 而 `StartsWith("ap") = true`——同一条路径，差别只在查不查终点节点的 `isEnd`。

实现的核心是节点结构。这段在做什么：声明 `TrieNode`，`children` 负责按字符分流，`isEnd` 负责标记单词结尾：

```go
type TrieNode struct {
    children map[rune]*TrieNode // 子节点映射：字符 → 子节点指针
    isEnd    bool               // 标记是否为单词结尾
}
```

`map[rune]` 而非 `[26]*TrieNode` 是通用性首选：天然支持 Unicode（中文、emoji、大小写），不受字符集限制；纯小写场景数组更快但通用性差。

## How It Works

机制的主线是：**字符进 children 分流，isEnd 标记终点**。Search 和 StartsWith 的差别只在一次布尔判断，Autocomplete 只是"定位 + 收集"。

### Insert：O(L) 沿路径建节点

从 root 逐字符下探，字符不存在则创建新节点，末尾置 `isEnd = true`。插入 `apple / app / application / apply` 后，前缀 `app` 只存一份——共享前缀是空间效率的全部来源。

插入 `app` 和 `apple` 后，`app` 节点 `isEnd=true`，`appl` 节点 `isEnd=false`。

### findNode：所有查询共用的下探

```go
func (t *Trie) findNode(prefix string) *TrieNode {
    node := t.root
    for _, ch := range prefix {
        if _, ok := node.children[ch]; !ok {
            return nil // 路径断裂
        }
        node = node.children[ch]
    }
    return node
}
```

三个查询操作都从 `findNode` 派生：`Search = findNode != nil && isEnd`（两个条件缺一不可）；`StartsWith = findNode != nil`（只看路径）；`Autocomplete` 先 `findNode` 定位，再收集。

输出里 `Search("app")` 走的就是 `root → a → p → p` 这条路径，第二个 `p` 的 `isEnd=true` 正是返回 `true` 的原因；而 `Search("appl")` 同样走得到该节点，只因 `isEnd=false` 返回 `false`。

### Autocomplete：定位 + 字典序 DFS

`findNode(prefix)` 花 O(L) 走到前缀终点，从那里做 DFS（深度优先搜索——沿一条分支走到底再回头）收集所有 `isEnd=true` 的路径。children 每层先排序再遍历，结果天然按字典序；攒够 k 个即剪枝（提前停止、不再继续搜索）。

输出验证：`Autocomplete("app", 5)` 返回 `[app, apple, application, apply]`（只补全 `app` 之后的后缀），空前缀 `Autocomplete("", 3)` 等价于"全树前 3 个单词"。

### 关键字段一览

| 字段/方法 | 含义 | 为什么重要 |
|---|---|---|
| `children map[rune]*TrieNode` | 字符 → 子节点 | 前缀分流点；map 天然支持任意 rune |
| `isEnd bool` | 单词结尾标记 | 没有它，`Search("appl")` 会误判为 true |
| `root` | 不存字符的哨兵 | 统一处理空前缀 `""` |
| `Autocomplete(p, k)` | 最多 k 个补全 | `len(results) >= k` 剪枝，避免扫全树 |

## Pitfalls & Q&A

以下条目都来自手写实现时的真实失误，每条按现象、原因、解法展开。

### 踩坑清单

- **Search 忘查 isEnd**。现象：`Search("appl")` 误判为 true。原因：只验证了路径存在，没验证终点是单词结尾——路径存在不等于单词存在。解法：Search 必须同时满足 `findNode != nil && isEnd`（真实输出第 2 组）。
- **Autocomplete 拼接时丢了前缀**。现象：补全结果里只剩 `le / ication / y` 一类残片。原因：DFS 里 path 只含前缀之后的部分，返回时没拼回 `prefix`。解法：结果统一为 `prefix + string(path)`。
- **children 遍历不排序**。现象：补全结果每次运行顺序都可能不同。原因：map 遍历是随机序。解法：DFS 每层先对 children 排序再遍历。
- **append 结果存到外层变量**。现象：DFS 收集到的路径相互污染、内容错乱。原因：`dfs(children[ch], append(path, ch))` 依赖"递归内先消费再扩容"的用法，把 append 结果存到外层变量会踩 slice 共享别名坑（多个变量指向同一底层数组，参见 [01_slices](../../internals/01_slices/)）。解法：让 append 结果只作为参数传入下一层递归。

### 深入问答

**Q：Trie 和 map 相比，前缀查询差在哪？**
A：`StartsWith`/`Autocomplete` 在 map 上只能 O(n·L) 全量扫描所有 key；Trie 只走一条 O(L) 路径，补全再花 O(k) 的 DFS。反过来，精确查找 map 平均 O(1) 优于 Trie 的 O(L)。

Trie 擅长前缀操作，map 擅长精确查找，二者互补。

**Q：各操作的时间复杂度？**
A：Insert / Search / StartsWith 都是 O(L)，与词典里存了多少单词无关；Autocomplete 是 O(L + k·L_avg)（定位 + 收集 k 个单词），通常简写 O(L + k)。

空间最坏 O(总字符数)，共享前缀显著节省（`app` 存一次服务 3 个单词）。

**Q：为什么用 `map[rune]` 而不是 `[26]*TrieNode`？**
A：map 不受字符集限制，天然支持 Unicode，通用性首选；确定的 26 小写字母场景数组无哈希开销更快。按字符集是否确定来选。

**Q：如何实现 Delete？为什么不能只置 `isEnd=false`？**
A：只置 `isEnd=false` 的话节点留在树上，空间只增不减。完整做法：递归到单词终点，`isEnd=false`；回溯时若节点无 children 且 `isEnd=false`，则从父节点 `delete` 掉，逐层向上。

要点：只有"无子节点且不是别的单词结尾"的节点才能物理删除。
