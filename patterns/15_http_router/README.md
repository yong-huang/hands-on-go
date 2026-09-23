# 15 · Mini HTTP Router：请求进来之后，路由器到底做了什么

> HTTP 路由是 Web 框架的心脏：把 `GET /users/42` 这样的请求准确送到对应的 handler，并把路径里的 `42` 抽出来交给业务代码。标准库 `ServeMux` 长期不支持路径参数（Go 1.22 才补上），所以"手写一个路由器"是 Go 后端的经典练习。本实验用不到 200 行代码实现方法分派、静态/参数段匹配、params 注入 context 与中间件链，并用 `httptest` 把每次匹配结果真实打印出来。

## 1. 为什么需要它

`http.ListenAndServe(":8080", router)` 的第二个参数只要是实现了 `http.Handler` 接口（唯一方法 `ServeHTTP(ResponseWriter, *Request)`）的对象即可——路由器的全部职责都浓缩在这个方法里：**查表 → 匹配 → 注入参数 → 包中间件 → 执行 → 兜底 404**。手写一遍之后，gorilla/mux、chi、echo 的行为差异你都能从原理上解释。

## 2. 总览：核心机制一图看懂

![Mini HTTP Router 一次请求的匹配与分发数据流](images/router.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/patterns/15_http_router/images/router.html)
> （或本地打开 [`images/router.html`](images/router.html)）。

**心智模型一句话**：路由匹配是一条数据流——请求先按 `Method` 在路由表里找到候选子树，再对路径逐段比对（静态段精确相等、`:param` 段捕获任意非空值），匹配成功则把 `params` 写进 request context，穿过中间件链抵达 handler；整条路由表遍历完无命中就走 `http.NotFound`（404）。

## 3. 快速开始

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

请求用 `httptest.NewRequest` + `httptest.NewRecorder` 模拟，不占端口；`[LOG]`/`[AUTH]` 是中间件打印的，可以直观看到"先注册的先执行"。

## 4. 核心概念

### 4.1 route 与 Router

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

`addRoute` 时用 `extractParamNames` 把 `:param` 名字一次性解析好存在路由条目里，匹配阶段不用重复扫 pattern。图里 stage 1 的"按方法分派"对应 `ServeHTTP` 的第一层过滤：`rt.method != req.Method` 直接 `continue`，等价于把路由表按方法分成几棵子树再遍历。

### 4.2 逐段匹配 matchRoute

把 pattern 和 path 都按 `/` 切段：段数不同立刻失败；静态段必须逐段精确相等（`/static/a × /static/b → NO MATCH`）；`:` 开头的段无条件捕获当前值，按预解析的顺序写入 `params`（如 `:id→42`、`:pid→7`）。这是**线性遍历匹配**，复杂度 O(n×L)；radix tree 是路由多时的优化方向（见 §7）。

### 4.3 params 注入 context

匹配成功后把每个 `k:v` 依次 `context.WithValue` 进 request context（共用一个 `map[string]string`），`req = req.WithContext(ctx)` 生成新请求；handler 里用包级函数 `Param(r, "id")` 读出。参数跟着请求走、类型安全收口在一个 key 上，这就是 gorilla/mux `Vars(r)`、chi `URLParam(r, "id")` 的同款思路。

### 4.4 中间件链

`Use(A)`、`Use(B)` 之后，`ServeHTTP` **从后往前**包裹：`handler = A(B(C(origin)))`，执行时从外到内恰好是 A → B → C → 业务 handler，与注册顺序一致。输出里 `[LOG]` 永远在 `[AUTH]` 前面就是这个原因。注意中间件包装发生在**每次匹配成功后**，而 `r.middlewares` 本身没有锁保护——本例中它在请求到来前注册完毕，实际项目应在启动期冻结中间件列表。

## 5. 关键字段与坑清单

| 字段/函数 | 含义 | 为什么重要 |
|---|---|---|
| `route.paramNames` | pattern 里的参数名，注册时预解析 | 匹配时按序对号入座，避免重复扫描 |
| `matchRoute` 返回值 | `(bool, map[string]string)` | 匹配与参数提取一次完成 |
| `paramsKey contextKey` | 自定义 context key 类型 | 用私有类型防其他包的 key 碰撞 |
| `r.mu.RLock()` | ServeHTTP 全程持读锁 | 并发请求可并行读路由表 |
| `http.NotFound` | 兜底响应 | 遍历完无 `method+path` 同时命中才走这里 |

坑清单：

- **段数敏感**：`/users/42/` 尾部多一个 `/` 会多出空段，与 `/users/:id` 段数不等直接 404——本实现没有做尾部斜杠归一化
- **参数捕获顺序依赖注册时的段顺序**：`paramIdx` 按 pattern 从左到右推进，pattern 与 paramNames 必须同源生成
- **没有 405 语义**：路径匹配但方法不匹配时继续遍历、最终返回 404；生产路由器（及 Go 1.22+ ServeMux）会返回 `405 Method Not Allowed` 并带 `Allow` 头
- **`Use` 无锁**：`routes` 有写锁而 `middlewares` 没有，启动后追加中间件存在数据竞争
- **中间件包裹每请求重做一遍**：路由多、中间件多时可预热缓存包裹结果

## 6. 文件结构

```text
patterns/15_http_router/
├── README.md                    # 本文件：原理 + 运行指引 + 深入要点
├── http_router.go               # 主教材：路由表 → matchRoute → ServeHTTP → 中间件 → httptest 演示
└── images/
    ├── router.dataflow.json     # 图源：请求→方法分派→逐段匹配→params 注入→handler（Typed JSON IR）
    ├── router.html              # 交互版：可缩放/高亮连线的自包含页面
    └── router.svg               # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：和标准库 `http.ServeMux` 相比，这个路由器差在哪？**
A：老版 ServeMux 只做前缀匹配 + 精确匹配，无路径参数、方法要在 handler 里自己判断；Go 1.22 起 ServeMux 支持 `GET /users/{id}` 形式的方法+通配符模式并返回 405，但仍不支持正则约束、中间件与路由分组。手写路由器（以及 chi/echo）的价值在于参数捕获、中间件链和可组合的路由组。

**Q：线性遍历 vs Trie/radix tree 怎么选？**
A：路由少（几十条）时线性 O(n×L) 足够、实现最简单；radix tree 压缩公共前缀，查找只与路径长度 O(L) 相关，是 chi、gin 的方案。注意 gorilla/mux 实际是正则+线性遍历，并不用 radix tree。建议先写线性版讲清语义，再谈树化优化。

**Q：`GET /users/42` 但只注册了 `POST /users/:id`，应该返回什么？**
A：严格说应返回 405 并带 `Allow: POST` 头；本实现为简单起见方法不匹配就跳过，最终落到 404。能主动指出这个语义差异是加分项。

**Q：路径参数为什么放 context 而不是全局变量或闭包？**
A：HTTP handler 是并发执行的，参数必须随请求生命周期传递；context 挂在 `*http.Request` 上天然请求级隔离，且不改变 `http.HandlerFunc` 签名，任何标准中间件都能无缝套用。代价是 key 需用私有类型防碰撞、取值要做类型断言。

**Q：手写时最容易踩的错有哪些？**
A：① 切段后不比较段数，导致 `/a/b` 错配 `/a/:x/c` 的前半；② 参数值忘了写回（或写错下标顺序）；③ 中间件从前往后包裹，导致执行顺序与注册顺序相反；④ `:param` 匹配了空段（本实现中 `""` 也会被捕获，严谨实现应拒绝空段）；⑤ 忘记 `req = req.WithContext(ctx)`，handler 里取不到参数。

## 8. 总结

路由器的全部行为都能从"线性路由表 + 逐段匹配"推出来：方法先行过滤、静态段精确、参数段捕获、context 注入、中间件正序执行、兜底 404。记住验证方法——`go run http_router.go` 把路由表、每次匹配与参数提取全部打印出来，比背结论可靠得多。

下一篇 [16_graceful_shutdown](../16_graceful_shutdown/) 看服务端另一道必考题：收到 SIGINT 后如何不丢请求地优雅退出。
