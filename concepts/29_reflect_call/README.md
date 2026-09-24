# 29 · 反射动态创建与调用：reflect 不仅能看，还能造与调

> 反射最常见的用法是"读"结构体字段（见 [27_reflect_basics](../27_reflect_basics/)），但它还有更主动的一面：在只有类型信息、没有任何实例的运行时，凭空 `reflect.New` 造出实例、按名字字符串 `MethodByName` 找到方法、`Call` 打包参数调用并拆包返回值——这就是插件系统、RPC 框架与序列化库的核心机制。代价是慢约一个数量级，且失去编译期检查。

## 1. 为什么需要它

写业务代码时你永远不需要反射调用：`g.Greet("Tom")` 一行就够，编译器还能帮你查签名。但当你写的代码要去调用**别人在运行时才告诉你**的函数——插件按名字注册命令、RPC server 按方法名派发、测试框架按约定名执行用例——编译期根本不存在这次调用。`reflect` 提供了这层"运行时函数指针"能力：类型当图纸、名字当索引、`Call` 当扳手。理解它，你才能看懂 net/rpc、text/template 和各类依赖注入框架在做什么。

## 2. 总览：核心机制一图看懂

![反射动态创建与调用时序图](images/refcall.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/29_reflect_call/images/refcall.html)
> （或本地打开 [`images/refcall.html`](images/refcall.html)）。

**心智模型一句话**：`reflect.Type` 是图纸，`reflect.New` 照图纸造实例，`MethodByName` 用字符串当索引进方法表，`Call` 把参数与返回值统一打包成 `[]reflect.Value` 搬运——普通函数与方法走的是同一套流程。

## 3. 快速开始

```bash
cd concepts/29_reflect_call
go run main.go
```

预期输出（节选）：

```text
========== 1. reflect.New：按类型动态创建实例 ==========
动态创建: Dr. Who | 字段 = true

========== 2. MethodByName + Call：动态调用方法 ==========
方法存在: true
动态调用结果: Dr. Watson

========== 3. 动态调用普通函数 ==========
签名: func(int, int) int | in = 2 | out = 1
add(2, 40) = 42

========== 4. 对照：直接调用 vs 反射调用 ==========
直接调用: Dr. Watson | 与反射结果一致: true
两种方式各执行 100000 次（数量级差异请在基准测试中量化）
```

## 4. 核心概念

### 4.1 reflect.New：从类型到实例

`reflect.New(t)` 接收 `reflect.Type`，返回一个指向新分配零值的 `Value`（指针类型）。要写字段必须先 `Elem()` 解引用：`Elem().FieldByName("Prefix").SetString("Dr. ")`。最后 `Interface()` 把 `Value` 还原成 `any`，再用类型断言取回静态类型——注意 `New` 给的是指针，断言目标要写 `*Greeter`。

### 4.2 MethodByName：字符串当索引进方法表

`v.MethodByName("Greet")` 按名字查方法，返回一个"绑定了接收者"的函数 `Value`。名字拼写错误编译器帮不上忙，所以查完先 `IsValid()` 确认存在。在指针值上查询能覆盖值接收者与指针接收者的完整方法集；在值上查询则只能看到值接收者方法。

### 4.3 Call：参数与返回值都是 []reflect.Value

调用前把每个参数用 `reflect.ValueOf(x)` 包装进切片；`Call` 执行后返回 `[]reflect.Value`，按声明顺序拆包：`results[0].String()`、`.Int()` 等。签名不匹配不会静默——个数或类型对不上直接 panic，动态调用前可以先 `fn.Type()` 检查 `NumIn/NumOut`。

### 4.4 普通函数与方法是同一套玩法

`reflect.ValueOf(add)` 直接把函数转成 `Value`，后面的 `Type` 检查与 `Call` 流程和方法完全一致。这就是 `template.Execute` 调用数据上的任意方法、RPC 框架按 "Service.Method" 字符串派发到 handler 的全部原理。

### 4.5 代价：慢约一个数量级，且失去编译期检查

反射调用要做名字查找、参数打包、接口装箱与签名校验，基准测试通常显示比直接调用慢 10 倍上下；更重要的是错误从编译期推迟到了运行时。热路径不要用反射调用，框架代码在初始化期做一次、缓存住 `Value` 再用是常见折中。底层 `Value` 的内存布局与调用机制见 [../../internals/27_reflect/](../../internals/27_reflect/)。

## 5. 关键字段与坑清单

| API | 含义 | 为什么重要 |
|---|---|---|
| `reflect.New(t)` | 分配零值，返回指针 Value | 从类型凭空造实例的唯一入口 |
| `ptr.Elem()` | 解引用拿到可 Set 的本体 | 不 Elem 直接 FieldByName 会 panic |
| `MethodByName(name)` | 按名字查绑定方法 | 无效名返回零 Value，用 IsValid 判断 |
| `Call(args []Value) []Value` | 调用并取回结果 | 参数必须逐个 ValueOf 包装 |
| `ValueOf(fn).Type()` | 动态读签名 | NumIn/NumOut 先检查再调用，防 panic |

坑清单：

- `reflect.New(t).Interface().(Greeter)` 会 panic——New 返回的是 `*Greeter`，断言要写指针类型
- 在 `Value`（非指针）上 `MethodByName` 查指针接收者方法会拿不到；要查就传指针
- 参数切片里的 `Value` 类型必须与签名逐位匹配（int ≠ int64），错了运行时 panic
- `Call` 的返回值要按序拆包成具体类型（`.String()`/`.Int()`），类型用错得到零值而非报错时，先怀疑取错了下标
- 方法名必须**导出**（大写开头）：未导出方法不在反射方法集里，`IsValid` 返回 false

## 6. 文件结构

```text
concepts/29_reflect_call/
├── README.md                  # 本文件：原理 + 运行指引 + 深入要点
├── main.go                    # 演示：New 建实例 / MethodByName+Call 调方法 / 动态调函数 / 直接调用对照
└── images/
    ├── refcall.sequence.json  # 图源：五步动态调用管线（Typed JSON IR）
    ├── refcall.html           # 交互版：自包含单文件，可缩放/切换主题
    └── refcall.svg            # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么 `reflect.New` 返回指针而不直接返回值？**
A：指针让实例可寻址、可 `Set`。若返回值，`Elem()` 得到的是不可地址化的临时值，`FieldByName(...).SetXxx` 会直接 panic——与"`x := v.FieldByName` 后不能 Set 当 v 来自 map"是同一条可寻址性规则。

**Q：`Call` 和 `Method`/`Call` 有什么区别？什么时候用后者？**
A：`Method(i)` 按下标取方法，`MethodByName` 按名字取；前者适合遍历方法集（如自动注册路由），后者适合按约定名调用（如 "Handle" + 命令名拼接）。多次调用同一方法时应把方法 `Value` 缓存起来，省掉每次的名字查找。

**Q：反射调用到底慢在哪？**
A：每步都有开销：名字哈希查方法表、参数逐个装箱成 `reflect.Value`（可能触发堆分配）、运行时签名校验、通过 `callReflect` 走一条与编译器生成代码完全不同的慢速调用路径。实测常见差距在 10~100 倍，热路径的正确姿势是初始化期解析一次并缓存，或代码生成。

**Q：既然又慢又容易 panic，标准库为什么大量用它？**
A：慢是"每次调用"的成本，框架用它的是"每条路由/每个类型解析一次"的低频路径，随后高频执行走的仍是普通函数值。用运行时的灵活性换编译期的安全性，在框架边界是划算的交易——你自己的业务代码则应停在类型断言（见 [26_iface_assert](../26_iface_assert/)）就够了的那一层。

## 8. 总结

反射的完整闭环是三步：`New` 从类型造实例、`MethodByName` 按名字拿方法、`Call` 打包调用并拆包结果——普通函数只是少了接收者的特例。记住两条底线：调用前先 `IsValid`/`Type` 自检防 panic，热路径缓存 `Value` 或干脆不用。

下一篇 [30_custom_marshal](../30_custom_marshal/) 看反射的实战收官：实现 `MarshalJSON/UnmarshalJSON` 接口接管序列化，把 28~29 两讲的反射知识落到一个真实场景里。
