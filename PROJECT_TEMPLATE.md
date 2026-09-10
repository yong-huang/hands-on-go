# PROJECT_TEMPLATE：如何给 hands-on-go 新增一个实验

本系列已定型为统一结构。新增实验（无论 internals 还是 interview 线）照本指南执行，
保证读者在任何一个实验里学到的"怎么跑、去哪看图、README 怎么读"完全一致。

## 1. 取号与建目录

- 目录名 = `NN_小写下划线短名`，编号在各自线内递增（internals 31 起、interview 21 起）。
  **只追加，不重排、不挖号**——旧链接永不失效。
- 容器目录固定 `internals/`（语言底层）与 `interview/`（面试手写），不新增第三条线；
  两线的区别在 README 的"面试要点"与"手写易错点"权重，不在文件结构。

```text
internals/31_your_topic/
├── README.md                      # 主教材（8 个编号章节，见 §3）
├── main.go                        # 演示程序：可 go run；支持 -json 输出状态快照则更佳
├── viz.py                         # （可选）仅实测数据图；无数据图就不要这个文件
└── images/
    ├── <fig>.<type>.json          # 图源 Typed JSON IR（type ∈ architecture/workflow/sequence/dataflow/lifecycle）
    ├── <fig>.html                 # 交互版自包含单文件
    ├── <fig>.svg                  # README 内嵌的双主题矢量图
    └── <name>.png                 # （可选）matplotlib 实测数据图
```

## 2. 演示程序约定

- 纯标准库、单文件、`go run main.go` 直接可跑；每一步"在看什么"写成中文注释。
- 结论必须来自程序输出，不要写文档里看不着的数；地址类数值每次运行都变，
  README 里用 `0x…198` 缩写并注明。基准/曲线类数据用 `-json` 快照交给 viz.py 实测绘制。

## 3. README 骨架（8 个编号章节，缺一不可）

```
# NN · 主题名：一句话副标题
> 引言 blockquote：本实验解决什么痛点（从上一篇的延伸切入），3~4 行
## 1. 为什么需要它
## 2. 总览：核心机制一图看懂      ← 内嵌主 SVG + 心智模型一句话 + 交互版链接
## 3. 快速开始                    ← cd 命令 + 预期输出节选（真实运行摘录）
## 4. 核心概念                    ← 2~5 小节；其余图随节走，每图配两三句"怎么看"
## 5. 关键字段与坑清单            ← 字段表 + 坑清单（每条坑都要能复现）
## 6. 文件结构                    ← 目录树逐文件注释（与磁盘一致，改完自查）
## 7. 面试要点                    ← 4~5 个问答；interview 线额外覆盖复杂度/边界/手写易错点
## 8. 总结                        ← 收束主线 + 下一篇相对链接 ../<NN+1_dir>/
```

每个内嵌 SVG 下方固定加：

```markdown
> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/NN_xxx/images/<fig>.html)
> （或本地打开 [`images/<fig>.html`](images/<fig>.html)）。
```

## 4. 图三件套管线

每实验 1~2 张聚焦图（一节一图，不凑数）。机制/流程/状态用架构图图源（Typed JSON IR）表达；
**实测数据（基准 ns、内存曲线）用 matplotlib PNG，不要硬塞进架构图**。

```bash
A=~/.agents/skills/archify/bin/archify.mjs
S=~/.agents/skills/hands-on-series/scripts
cd internals/31_your_topic
# 图源由 archify skill 生成；写图源前读该 skill 的 schema 与示例；workflow 用 schema_version 2
node "$A" validate <type> images/<fig>.<type>.json --quality showcase --json   # 0 错 0 警才算过
node "$A" deliver  <type> images/<fig>.<type>.json images/<fig>.html --quality showcase --json
node "$A" visual-check images/<fig>.html --json                                # 四视口零溢出
node "$S/export-svg.mjs" "$PWD/images/<fig>.html" "$PWD/images/<fig>.svg"      # 双主题 SVG
node "$S/pixel-center.mjs" "$PWD/images/<fig>.svg"                             # 像素居中（绝对路径）
rm -f images/*.visual-check.*                                                  # 旁路证据不入库
```

硬性要求：`quality_profile: "showcase"`、`locale: "zh-CN"`、节点 ≤ 12、一条主路径、
自动路由优先（被诊断了才加 route/labelAt）、不写 `visual_preset`/`subtitle`。
验收线：左右留白差 ≤2pp、质心 ±5%；`--optical` 只救左偏且会把留白差撑大——
质心/留白互斥时回图源重排节点列，不要硬拗。

## 5. 数据图（viz.py）约定

- 只画实测：`go run main.go -json` 取数 → matplotlib 绘制 → `savefig('images/xxx.png')`。
- 中文字体链 `['PingFang SC', 'Hiragino Sans GB', 'Arial Unicode MS', 'Heiti SC', 'sans-serif']`。
- 不用 `np.random` 之类编造数据；模拟推算的图要在标题与 README 注明"模拟值"。

## 6. 收尾自检清单

- [ ] `go run main.go` 与 README §3 输出一致；`go build` 通过
- [ ] 图源 showcase 0 错 0 警、visual-check 全过、SVG 居中双达标
- [ ] README 8 节齐全；每个 SVG/PNG 引用可解析；§6 文件树与磁盘一致
- [ ] 实验根目录无 PNG/二进制/visual-check 残留
- [ ] 根 README 的实验表加一行；本文件不需要改
