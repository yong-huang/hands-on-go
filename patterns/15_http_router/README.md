# 15 · Mini HTTP Router：请求进来之后，路由器到底做了什么

> HTTP 路由是 Web 框架的心脏：把 `GET /users/42` 这样的请求准确送到对应的 handler（处理 HTTP 请求并写出响应的函数），并把路径里的 `42` 抽出来交给业务代码。标准库 `ServeMux` 长期不支持路径参数（Go 1.22 才补上），所以"手写一个路由器"是 Go 后端的经典练习。本实验用不到 200 行代码实现方法分派、静态/参数段匹配、params 注入 context 与中间件链，并用 `httptest` 把每次匹配结果真实打印出来。

## Background

Go 标准库自带的 `http.ServeMux`（请求多路复用器——按注册的模式把请求分发给对应 handler）长期只支持前缀匹配和精确匹配，没有路径参数。想要 `/users/42` 里的 `42`，只能在 handler 里手动切 `r.URL.Path` 字符串、逐段解析再转类型。

这段解析逻辑会在每个需要参数的 handler 里重复一遍：路径格式一变（比如从 `/users/42` 变成 `/users/42/posts/7`），所有相关 handler 都要跟着改；方法区分也只能写成一长串 `if r.Method == ...` 判断。

社区长期用第三方路由器补位：gorilla/mux、chi、echo 都在 `http.Handler` 这一层补齐了路径参数这类能力，标准库直到 Go 1.22 才跟进。要分辨这些库的行为差异，最直接的路径是把路由器本身手写一遍。

## What

**HTTP 路由器**是一种实现 `http.Handler` 接口的请求分发组件，用来把"方法 + 路径"映射到对应的处理函数、并把路径中的动态片段交给业务代码。

接口唯一的 `ServeHTTP(ResponseWriter, *Request)` 方法是 `http.ListenAndServe(":8080", router)` 对第二个参数的全部要求，路由器的全部职责都浓缩在这个方法里：**查表 → 匹配 → 注入参数 → 包中间件 → 执行 → 兜底 404**。

心智模型一句话：路由匹配是一条数据流——请求先按 `Method` 在路由表里找到候选子树，再对路径逐段比对：静态段必须精确相等，`:param` 段捕获任意非空值。

匹配成功后，`params`（从路径捕获的键值对，如 `id=42`）被写进 request context（挂在请求上、随请求生命周期传递的标准库键值容器），穿过中间件链（在业务 handler 前后执行日志、鉴权等统一逻辑的包装函数）抵达业务代码；整张表遍历完无命中就走 `http.NotFound`（404）。

可以把路由器想象成公司前台：先看来访方式放不放行（方法过滤），再按要找的部门逐层核对门牌（逐段匹配），把房间号写在访客证上（参数注入）再放行。但和前台不同的是：路由器同时接待大量并发请求且互不干扰，对查无此处的路径一律返回固定的 404，不做现场协商。

## When to Use

这节回答"哪些活适合这不到 200 行的路由器，哪些应该换工具"。

典型场景：

- **内部工具、小型 API**：路由总共几十条，一个文件就能装下方法分派和参数捕获，不必为此引入依赖。
- **给一组路径加统一前置处理**：日志、鉴权这类逻辑通过中间件链挂载，注册顺序即执行顺序。
- **读第三方路由器源码之前**：先手写一遍同样的语义，gorilla/mux、chi、echo 的行为差异都能从原理上解释。

何时不用：

- 路由只有一两条固定路径：直接在 handler 里判断 `r.URL.Path`，或用标准库 `ServeMux`，更显式。
- 需要正则约束、路由分组或生产级 405 语义：本实现没有这些，应直接选成熟实现（见下表）。

| 方案 | 差异 | 什么时候选它 |
|------|------|------|
| 本实现的线性路由表 | 逐条比对，条数越多越慢；代码不足 200 行 | 学习语义；几十条以内的小服务 |
| 标准库 ServeMux（Go 1.22+） | 支持 `GET /users/{id}` 通配符并返回 405，但无中间件与路由分组 | 不想引入第三方依赖的常规服务 |
| gorilla/mux | 正则 + 线性遍历，不使用 radix tree（压缩前缀树——合并只有一个子节点的节点，减少查找步数） | 需要正则约束路由时 |
| chi / gin | radix tree 压缩公共前缀，查找耗时只与路径长度相关；chi 还提供中间件链与可组合的路由组 | 路由数量大、对分发耗时敏感时 |

## Quick Start

前置条件：只需安装 Go，无任何第三方依赖。

```bash
cd patterns/15_http_router
go run http_router.go
```

预期输出（节选）：

```text
--- Routing Table ---
Method Pattern                        ParamNames
GET    /users/:id/posts/:pid          [id pid]
POST   /users                         []

--- Fake Requests ---
  GET /users/42/posts/7
    [LOG] GET /users/42/posts/7
    [AUTH] checking token for /users/42/posts/7
    Status: 200
    Body:   Get Post: userId=42, postId=7

  GET /notfound
    Status: 404
    Body:   404 page not found

--- Param Extraction Demo ---
  /users/:id  ×  /users/123  →  map[id:123]
  /users/:id/posts/:pid  ×  /users/42/posts/7  →  map[id:42 pid:7]
  /static/a  ×  /static/b  →  NO MATCH
```

请求用 `httptest.NewRequest` + `httptest.NewRecorder` 模拟——`httptest` 是标准库的 HTTP 测试工具包，不监听端口即可构造请求、记录响应。`[LOG]`/`[AUTH]` 是中间件打印的，可以直观看到"先注册的先执行"。

输出分三段：路由表、模拟请求的结果、参数提取演示。第三段里 `map[id:42 pid:7]` 是怎么算出来的，How It Works 会逐步对应。

## How It Works

### 注册期：路由表与参数名预解析

```go
type route struct {
    method     string            // GET / POST / PUT / DELETE
    pattern    string            // /users/:id/posts/:pid
    handler    http.HandlerFunc
    paramNames []string          // ["id", "pid"]，注册时预解析
}
type Router struct {
    routes      []route                                    // 线性路由表
    middlewares []func(http.HandlerFunc) http.HandlerFunc
    mu          sync.RWMutex                               // 注册写锁 / 匹配读锁
}
```

这段在做什么：`Router` 持有一张线性路由表 `routes` 和中间件列表 `middlewares`；每条 `route` 在 `addRoute` 时就用 `extractParamNames` 把 `:param` 名字解析进 `paramNames`，匹配阶段不必重复扫描 pattern。

`mu sync.RWMutex`（读写锁——允许多个读者同时进入、写者独占）让并发请求可以并行读表。

### 匹配期：方法过滤 + 逐段比对

`ServeHTTP` 的第一层过滤是按方法分派：`rt.method != req.Method` 直接 `continue`，等价于把路由表按方法分成几棵子树再遍历。

`matchRoute` 把 pattern 和 path 都按 `/` 切段后逐段比对：段数不同立刻失败；静态段必须精确相等；`:` 开头的段无条件捕获当前值，按预解析的 `paramNames` 顺序写入 `params`（如 `:id→42`、`:pid→7`）。

这是线性遍历匹配，复杂度 O(n×L)——n 为路由条数、L 为路径段数（O 记号描述耗时随输入规模增长的趋势）。

你在输出里看到的 `/static/a × /static/b → NO MATCH` 就来自"静态段不相等立刻失败"；`map[id:42 pid:7]` 则是捕获值与 `paramNames` 对号入座的结果。

### 参数注入 context

匹配成功后把每个 `k:v` 依次 `context.WithValue` 进 request context（共用一个 `map[string]string`），再用 `req = req.WithContext(ctx)` 生成新请求；handler 里用包级函数 `Param(r, "id")` 读出。

参数跟着请求走、类型安全收口在一个 key 上——gorilla/mux 的 `Vars(r)`、chi 的 `URLParam(r, "id")` 是同款思路。

### 中间件链：从后往前包裹

`Use(A)`、`Use(B)` 之后，`ServeHTTP` **从后往前**包裹：`handler = A(B(C(origin)))`，执行时从外到内恰好是 A → B → C → 业务 handler，与注册顺序一致。输出里 `[LOG]` 永远在 `[AUTH]` 前面就是这个原因。

注意中间件包装发生在**每次匹配成功后**，而 `r.middlewares` 本身没有锁保护——本例中它在请求到来前注册完毕，实际项目应在启动期冻结中间件列表。

### 字段速查

| 字段/函数 | 含义 | 为什么重要 |
|---|---|---|
| `route.paramNames` | pattern 里的参数名，注册时预解析 | 匹配时按序对号入座，避免重复扫描 |
| `matchRoute` 返回值 | `(bool, map[string]string)` | 匹配与参数提取一次完成 |
| `paramsKey contextKey` | 自定义 context key 类型 | 用私有类型防其他包的 key 碰撞 |
| `r.mu.RLock()` | ServeHTTP 全程持读锁 | 并发请求可并行读路由表 |
| `http.NotFound` | 兜底响应 | 遍历完无 `method+path` 同时命中才走这里 |

## Pitfalls & Q&A

坑清单（每条：现象 + 原因 + 解法）：

- **尾部斜杠导致 404**：现象是 `/users/42/` 匹配不上 `/users/:id`。原因：尾部多一个 `/` 会多出空段，段数不等直接失败，本实现没有做尾部斜杠归一化。解法：匹配前统一裁掉尾部 `/`，或注册与请求保持同一写法。
- **参数值错位**：现象是取 `pid` 拿到 `id` 的值。原因：`paramIdx` 按 pattern 从左到右推进，pattern 改动后 `paramNames` 没有同步。解法：两者必须同源生成，不要手写其一。
- **该 405 的请求返回 404**：现象是 `GET /users/42` 在只注册了 `POST /users/:id` 时得到 404。原因：方法不匹配就跳过，没有实现 405（Method Not Allowed，方法不被允许）语义。解法：遍历时记录"路径命中但方法不符"，循环结束后返回 405 并带 `Allow` 头——生产路由器与 Go 1.22+ ServeMux 正是如此。
- **启动后追加中间件触发数据竞争**（数据竞争——多个 goroutine 同时访问同一变量且至少一个是写，结果不可预测）：现象是高并发下偶发异常。原因：`routes` 有写锁而 `middlewares` 没有。解法：在启动期冻结中间件列表，运行期不再修改。
- **路由与中间件多时每请求重复包裹**：现象是分发耗时随路由规模上升。原因：中间件包装发生在每次匹配成功后。解法：预热缓存包裹结果（预热——请求到来前提前算好并存起来）。

**Q：和标准库 `http.ServeMux` 相比，这个路由器差在哪？**

A：老版 ServeMux 只做前缀匹配 + 精确匹配，无路径参数、方法要在 handler 里自己判断；Go 1.22 起 ServeMux 支持 `GET /users/{id}` 形式的方法+通配符模式并返回 405，但仍不支持正则约束、中间件与路由分组。

手写路由器（以及 chi/echo）的价值在于参数捕获、中间件链和可组合的路由组。

**Q：线性遍历 vs Trie/radix tree 怎么选？**
A：路由少（几十条）时线性 O(n×L) 足够、实现最简单；radix tree 压缩公共前缀，查找只与路径长度 O(L) 相关，是 chi、gin 的方案。注意 gorilla/mux 实际是正则+线性遍历，并不用 radix tree。

建议先用线性版实现并验证语义，再考虑树化优化。

**Q：`GET /users/42` 但只注册了 `POST /users/:id`，应该返回什么？**
A：严格语义是 405 并带 `Allow: POST` 头，本实现为简单起见落到 404——修法见坑清单第 3 条。

**Q：路径参数为什么放 context 而不是全局变量或闭包（引用了外部变量的函数）？**
A：HTTP handler 是并发执行的，参数必须随请求生命周期传递；context 挂在 `*http.Request` 上天然请求级隔离，且不改变 `http.HandlerFunc` 签名，任何标准中间件都能无缝套用。

代价是 key 需用私有类型防碰撞、取值要做类型断言。

**Q：手写实现还有哪些容易漏掉的错误？**
A：坑清单之外另有两个：`:param` 匹配了空段——本实现中 `""` 也会被捕获，严谨实现应拒绝空段；以及忘记 `req = req.WithContext(ctx)`，导致 handler 里取不到参数。
