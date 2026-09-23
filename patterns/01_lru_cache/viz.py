#!/usr/bin/env python3
"""LRU Cache 数据图：运行时行为统计。
运行本脚本会调用 `go run lru_cache.go`，从真实输出解析操作计数。
"""

import subprocess
import sys
import re
from pathlib import Path

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

plt.rcParams["font.sans-serif"] = ["PingFang SC", "Hiragino Sans GB",
                                   "Arial Unicode MS", "Heiti SC", "sans-serif"]
plt.rcParams["axes.unicode_minus"] = False

HERE = Path(__file__).parent
IMAGES = HERE / "images"


def run_demo():
    out = subprocess.run(["go", "run", "lru_cache.go"],
                         cwd=HERE, text=True, capture_output=True)
    if out.returncode != 0:
        sys.exit("go run failed:\n" + out.stderr)
    return out.stdout


def draw_runtime(stdout):
    puts = len(re.findall(r"Put\(\d", stdout))
    evicts = stdout.count("淘汰key")
    gets = len(re.findall(r"Get\(\d", stdout))
    hits = len(re.findall(r"(?<!未)命中", stdout))
    misses = len(re.findall(r"未命中", stdout))

    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(11, 5))

    ops = ["Put", "Get", "Evict"]
    counts = [puts, gets, evicts]
    bars = ax1.bar(ops, counts, color=["#55A868", "#4C72B0", "#C44E52"], width=0.55)
    for b, c in zip(bars, counts):
        ax1.text(b.get_x() + b.get_width() / 2, c + 0.1, str(c),
                 ha="center", fontsize=12, fontweight="bold")
    ax1.set_title("操作类型分布", fontsize=12, fontweight="bold")
    ax1.set_ylabel("次数")
    ax1.set_ylim(0, max(counts) * 1.25)

    labels = ["命中", "未命中"]
    hvals = [hits, misses]
    wedges, _, _ = ax2.pie(hvals, labels=labels, autopct="%1.0f%%",
                        colors=["#55A868", "#C44E52"], startangle=90,
                        textprops={"fontsize": 12})
    ax2.set_title("Get 命中率统计", fontsize=12, fontweight="bold")

    fig.suptitle(f"LRU Cache 运行时行为（演示序列，共 {puts + gets} 次操作）",
                 fontsize=13, fontweight="bold")
    plt.tight_layout()
    IMAGES.mkdir(exist_ok=True)
    fig.savefig(IMAGES / "lru_runtime.png", dpi=150, bbox_inches="tight")
    plt.close(fig)
    print(f"OK images/lru_runtime.png  Put={puts} Get={gets} Evict={evicts} "
          f"命中={hits} 未命中={misses}")


if __name__ == "__main__":
    draw_runtime(run_demo())
