#!/usr/bin/env python3
# viz.py：真实测量三种计数方式的结果并画柱状图
# 数据来源：go run main.go -json（真实运行，不编造数据）
# 无锁版本的丢失更新是概率性的：单次运行可能碰巧得到 100，
# 所以本脚本真实运行 main.go -json 多轮（默认 50 次），
# 取三方各自的最小值作图——丢失量只会被低估，不会被夸大。
# 输出：images/race.png

import json
import subprocess
import sys
from collections import defaultdict
from pathlib import Path

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib import font_manager

HERE = Path(__file__).resolve().parent
ROUNDS = 50

# 中文字体链：macOS 优先 PingFang SC，其余平台依次回退
for f in ["PingFang SC", "Hiragino Sans GB", "Noto Sans CJK SC", "Microsoft YaHei", "Arial Unicode MS"]:
    if any(font.name == f for font in font_manager.fontManager.ttflist):
        plt.rcParams["font.family"] = f
        break
plt.rcParams["axes.unicode_minus"] = False

def one_round() -> dict:
    """真实运行一次 main.go -json，返回三方计数快照"""
    out = subprocess.run(
        ["go", "run", "main.go", "-json"],
        cwd=HERE, capture_output=True, text=True, check=True,
    )
    return json.loads(out.stdout)

def main():
    expect = None
    mins = defaultdict(lambda: 10**9)   # 每种方式在 50 轮中的最小值
    unlocked_runs = []                  # 无锁版本每轮的原始值（用于标注）
    for _ in range(ROUNDS):
        data = one_round()
        expect = data["expect"]
        for r in data["results"]:
            mins[r["name"]] = min(mins[r["name"]], r["value"])
            if "无锁" in r["name"]:
                unlocked_runs.append(r["value"])

    names = list(mins.keys())
    values = [mins[n] for n in names]
    fig, ax = plt.subplots(figsize=(8, 5), dpi=150)
    colors = ["#f87171", "#34d399", "#60a5fa"]
    bars = ax.bar(names, values, color=colors, width=0.55)

    lost_min = expect - min(unlocked_runs)
    lost_max = expect - max(unlocked_runs)
    for bar, n, v in zip(bars, names, values):
        note = f"丢失 {lost_min}~{lost_max} 次" if "无锁" in n else "精确"
        ax.text(bar.get_x() + bar.get_width() / 2, bar.get_height() + 0.8,
                f"{v}\n({note})", ha="center", va="bottom", fontsize=10)

    ax.axhline(expect, color="#94a3b8", ls="--", lw=1)
    ax.text(len(names) - 0.45, expect + 0.5, f"期望值 {expect}", fontsize=9, color="#64748b")
    ax.set_title(
        f"100 个 goroutine 并发加 1：{ROUNDS} 轮真实运行的最小计数结果\n"
        f"（无锁版本每次运行都不同：本轮实测最低 {min(unlocked_runs)}、最高 {max(unlocked_runs)}）",
        fontsize=12,
    )
    ax.set_ylabel("计数器最终值")
    ax.set_ylim(0, expect * 1.15)
    ax.spines[["top", "right"]].set_visible(False)

    out = HERE / "images" / "race.png"
    out.parent.mkdir(exist_ok=True)
    fig.tight_layout()
    fig.savefig(out)
    print(f"已生成 {out}（{ROUNDS} 轮最小值：{dict(mins)}）")

if __name__ == "__main__":
    sys.exit(main())
