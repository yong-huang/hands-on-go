# 04 · Trie 前缀树：children map[rune] + isEnd，一条查询路径

> Trie（发音 "try"，前缀树/字典树）是继哈希表、二叉树之后最常被要求手写的树形数据结构。它的核心优势是**共享公共前缀**，让前缀搜索、自动补全达到与字符串长度相关而与词典大小无关的效率。本实验用 Go 实现完整的 Insert / Search / StartsWith / Autocomplete，跑出真实输出，再把"一条查询路径"画成一棵看得见的树。

## 1. 为什么需要它

输入 `app`，搜索引擎瞬间给出 `apple / application / apply`——哈希表做不到这件事：它只能精确命中，前缀查询必须遍历所有 key。Trie 把"按字符逐级分流"变成结构本身：公共前缀只存一份，前缀操作（StartsWith、Autocomplete、最长前缀匹配）全部只走一条从根到叶的路径。

典型应用：自动补全（搜索引擎、IDE）、拼写检查、IP 路由表（最长前缀匹配）、T9 输入法。

## 2. 总览：核心机制一图看懂

![Trie 前缀树：children map[rune] + isEnd，高亮 app 查询路径](images/trie.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/patterns/04_trie/images/trie.html)
> （或本地打开 [`images/trie.html`](images/trie.html)）。

**心智模型一句话**：每个节点就是一个 `map[rune]*TrieNode` 加一个 `isEnd` 布尔——字符进 map 分流，`isEnd` 区分"只是前缀"和"完整单词"。图中高亮的是 `Autocomplete("app", 5)` 的 `findNode` 主路径：`root → a → p → p`，第二个 `p` 的 `isEnd=true` 正是 `Search("app")=true` 的原因；从 `l` 再向下 DFS 按字典序收集 `apple / apply / application`。

## 3. 快速开始

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

注意 `Search("appl") = false` 而 `StartsWith("ap") = true`——同一条路径，差别只在查不查终点节点的 `isEnd`。这正是 §2 图里想让你看见的东西。

## 4. 核心概念

### 4.1 TrieNode：children + isEnd

```go
type TrieNode struct {
    children map[rune]*TrieNode // 子节点映射：字符 → 子节点指针
    isEnd    bool               // 标记是否为单词结尾
}
```

- `map[rune]` 而非 `[26]*TrieNode`：天然支持 Unicode（中文、emoji、大小写），不受字符集限制；纯小写场景数组更快但通用性差。
- `isEnd` 区分"前缀"与"完整单词"：插入 `app` 和 `apple` 后，`app` 节点 `isEnd=true`，`appl` 节点 `isEnd=false`。
- root 是空节点，不存字符，所有实际字符从 `root.children` 开始。

### 4.2 Insert：O(L) 沿路径建节点

从 root 逐字符下探，字符不存在则创建新节点，末尾置 `isEnd = true`。插入 `apple / app / application / apply` 后，前缀 `app` 只存一份——共享前缀是空间效率的全部来源。

### 4.3 findNode：所有查询共用的下探

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

`Search = findNode != nil && isEnd`（两个条件缺一不可）；`StartsWith = findNode != nil`（只看路径）；`Autocomplete` 先 findNode 定位，再 DFS。

### 4.4 Autocomplete：定位 + 字典序 DFS

`findNode(prefix)` 花 O(L) 走到前缀终点，从那里 DFS 收集所有 `isEnd=true` 的路径；children 每层先排序再遍历，结果天然按字典序；攒够 k 个即剪枝停止。真实输出验证：`Autocomplete("app", 5)` 返回 `[app, apple, application, apply]`（只补全 `app` 之后的后缀），空前缀 `Autocomplete("", 3)` 等价于"全树前 3 个单词"。

## 5. 关键字段与坑清单

| 字段/方法 | 含义 | 为什么重要 |
|---|---|---|
| `children map[rune]*TrieNode` | 字符 → 子节点 | 前缀分流点；map 天然支持任意 rune |
| `isEnd bool` | 单词结尾标记 | 没有它，`Search("appl")` 会误判为 true |
| `root` | 不存字符的哨兵 | 统一处理空前缀 `""` |
| `Autocomplete(p, k)` | 最多 k 个补全 | `len(results) >= k` 剪枝，避免扫全树 |

坑清单：

- `Search` 忘查 `isEnd`：路径存在 ≠ 单词存在，`Search("appl")` 必须是 false（真实输出第 2 组）
- `Autocomplete` 拼接时别丢前缀：DFS 里是 `prefix + string(path)`，path 只含前缀之后的部分
- children 遍历是 map 随机序，不排序则补全结果每次运行顺序都可能不同
- `dfs(children[ch], append(path, ch))` 依赖"递归内先消费再扩容"的用法，若把 `append` 结果存到外层变量会踩 slice 共享别名坑（参见 [01_slices](../../internals/01_slices/)）
- 删除单词不能只置 `isEnd=false` 就完事：无子节点且非其他单词结尾的节点应自底向上回收，否则空间只增不减

## 6. 文件结构

```text
patterns/04_trie/
├── README.md                    # 本文件：原理 + 运行指引 + 深入要点
├── trie.go                      # 主教材：Insert/Search/StartsWith/findNode/Autocomplete + 演示
└── images/
    ├── trie.architecture.json   # 图源：children map + isEnd，app 查询路径高亮（Typed JSON IR）
    ├── trie.html                # 交互版：可缩放/高亮连线的自包含页面
    └── trie.svg                 # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：Trie 和 map 相比，前缀查询差在哪？**
A：`StartsWith`/`Autocomplete` 在 map 上只能 O(n·L) 全量扫描所有 key；Trie 只走一条 O(L) 路径，补全再花 O(k) DFS。反过来，精确查找 map 平均 O(1) 优于 Trie 的 O(L)。Trie 擅长前缀操作，map 擅长精确查找，二者互补。

**Q：各操作的时间复杂度？**
A：Insert / Search / StartsWith 都是 O(L)，与词典里存了多少单词无关；Autocomplete 是 O(L + k·L_avg)（定位 + 收集 k 个单词），通常简写 O(L + k)。空间最坏 O(总字符数)，共享前缀显著节省（`app` 存一次服务 3 个单词）。

**Q：为什么用 `map[rune]` 而不是 `[26]*TrieNode`？**
A：map 不受字符集限制，天然支持 Unicode，通用性首选；确定的 26 小写字母场景数组无哈希开销更快。能主动对比两种方案是加分项。

**Q：手写 Trie 最容易错的地方？**
A：三处——① `Search` 忘了 `isEnd` 判定；② 补全结果要拼 `prefix + path`，只返回 path 会丢前缀；③ children 是 map，DFS 前必须排序否则结果无序。另外空 Trie / 空前缀要靠不存字符的 root 哨兵统一处理。

**Q：如何实现 Delete？**
A：递归到单词终点，`isEnd=false`；回溯时若节点无 children 且 `isEnd=false` 则从父节点 `delete` 掉，逐层向上。要点：只有"无子节点且不是别的单词结尾"的节点才能物理删除。

## 8. 总结

Trie = 多叉树 + isEnd 标记，全部行为都能从"children 分流 + 终点标记"推出来：Search 和 StartsWith 的差别只在一次布尔判断，Autocomplete 只是"定位 + DFS"。手写时把 §5 的三个易错点守住，实现就稳了。

下一篇 [05_bloom_filter](../05_bloom_filter/) 看另一种"用结构换查询"的经典结构：布隆过滤器为什么能说"一定不存在"，却只敢说"可能存在"。
