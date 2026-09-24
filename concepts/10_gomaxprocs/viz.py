#!/usr/bin/env python3
# viz.py：真实测量 GOMAXPROCS=1/2/4/8 的耗时并画柱状图
# 数据来源：go run main.go -json（真实运行，不编造数据）
# 输出：images/gomaxprocs.png

import json
import subprocess
import sys
from pathlib import Path

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib import font_manager

HERE = Path(__file__).resolve().parent

# 中文字体链：macOS 优先 PingFang SC，其余平台依次回退
for f in ["PingFang SC", "Hiragino Sans GB", "Noto Sans CJK SC", "Microsoft YaHei", "Arial Unicode MS"]:
    if any(font.name == f for font in font_manager.fontManager.ttflist):
        plt.rcParams["font.family"] = f
        break
plt.rcParams["axes.unicode_minus"] = False

def measure() -> dict:
    """真实运行 main.go -json，采集每个 GOMAXPROCS 取值的耗时"""
    out = subprocess.run(
        ["go", "run", "main.go", "-json"],
        cwd=HERE, capture_output=True, text=True, check=True,
    )
    return json.loads(out.stdout)

def main():
    data = measure()
    ps = [r["p"] for r in data["results"]]
    ms = [r["ms"] for r in data["results"]]
    speedups = [r["speedup"] for r in data["results"]]

    fig, ax = plt.subplots(figsize=(8, 5), dpi=150)
    colors = ["#f87171", "#fbbf24", "#34d399", "#60a5fa"]
    bars = ax.bar([f"P={p}" for p in ps], ms, color=colors, width=0.6)

    for bar, m, s in zip(bars, ms, speedups):
        ax.text(bar.get_x() + bar.get_width() / 2, bar.get_height() + max(ms) * 0.02,
                f"{m:.1f} ms\n{max(s, 1):.2f}x",
                ha="center", va="bottom", fontsize=10)

    ax.set_title(
        f"GOMAXPROCS 对 CPU 密集任务耗时的影响（本机 {data['numcpu']} 核，机器相关、每次测量有波动）",
        fontsize=12,
    )
    ax.set_xlabel("GOMAXPROCS（P 个 goroutine 平分恒定的 32 次 fib(30)）")
    ax.set_ylabel("总耗时（ms，越低越好）")
    ax.spines[["top", "right"]].set_visible(False)
    ax.set_ylim(0, max(ms) * 1.22)

    out = HERE / "images" / "gomaxprocs.png"
    out.parent.mkdir(exist_ok=True)
    fig.tight_layout()
    fig.savefig(out)
    print(f"已生成 {out}（数据：{list(zip(ps, ms))}）")

if __name__ == "__main__":
    sys.exit(main())
