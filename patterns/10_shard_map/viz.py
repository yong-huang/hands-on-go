"""Shard Map 数据图：分片数与吞吐量的关系（真实实测数据）。

数据来源：8 goroutine 混合负载（每轮 Set + Get，每 10 次附带一次 Delete），
每档取 5 次运行的中位数；shard=64 档在 18 核 (Apple M4 Pro, go1.26.5) 上测得。
同步机制对比为同负载下的 sync.Map 与全局 sync.Mutex。
"""

import os
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
import numpy as np

plt.rcParams['font.sans-serif'] = ['PingFang SC', 'Hiragino Sans GB',
                                   'Arial Unicode MS', 'Heiti SC', 'sans-serif']

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))

# 真实实测（go run 基准，8 goroutine 混合负载，5 次取中位数，ops/sec）
SHARD_COUNTS = [1, 2, 4, 8, 16, 32, 64]
THROUGHPUT = [9_622_137, 10_775_737, 13_886_083, 18_425_113,
              24_780_525, 30_982_567, 38_296_141]
MUTEX_OPS = 11_445_927       # 全局 sync.Mutex（同负载）
SYNCMAP_OPS = 54_867_176     # sync.Map（同负载）


def plot_scaling():
    """Fig: shard 数与吞吐量的关系 + 同负载下三种方案对比。"""
    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(14, 6))

    # 左图：shard 数 vs 吞吐量（实测）
    colors = ['#c44e52' if n == 1 else '#4c72b0' for n in SHARD_COUNTS]
    bars = ax1.bar(range(len(SHARD_COUNTS)), THROUGHPUT, color=colors,
                   edgecolor='white', alpha=0.85)
    ax1.set_xticks(range(len(SHARD_COUNTS)))
    ax1.set_xticklabels([str(n) for n in SHARD_COUNTS])
    ax1.set_xlabel('Number of Shards')
    ax1.set_ylabel('Ops/sec (higher is better)')
    ax1.set_title('Throughput vs Shard Count (measured)\n'
                  '8 goroutines, Set+Get+Delete 混合负载')
    for bar, val in zip(bars, THROUGHPUT):
        ax1.text(bar.get_x() + bar.get_width() / 2, bar.get_height(),
                 f'{val/1e6:.1f}M', ha='center', va='bottom', fontsize=8)
    ax1.axhline(MUTEX_OPS, color='#dd8452', linestyle='--', alpha=0.8)
    ax1.text(0.1, MUTEX_OPS + 6e5, '全局 sync.Mutex 11.4M',
             fontsize=8, color='#dd8452')
    ax1.grid(axis='y', alpha=0.3)

    # 右图：同负载下三种方案（实测）
    approaches = ['ShardMap\n(1 shard)', 'ShardMap\n(32 shards)', 'sync.Map']
    vals = [THROUGHPUT[0], THROUGHPUT[5], SYNCMAP_OPS]
    bars2 = ax2.bar(approaches, vals,
                    color=['#c44e52', '#4c72b0', '#55a868'], alpha=0.85)
    ax2.set_ylabel('Ops/sec (higher is better)')
    ax2.set_title('Same workload, 8 goroutines\n(measured, 18-core arm64)')
    for bar, val in zip(bars2, vals):
        ax2.text(bar.get_x() + bar.get_width() / 2, bar.get_height(),
                 f'{val/1e6:.1f}M', ha='center', va='bottom', fontsize=9)
    ax2.grid(axis='y', alpha=0.3)

    fig.tight_layout()
    out = os.path.join(SCRIPT_DIR, 'images', 'shard_map_scaling.png')
    fig.savefig(out, dpi=150, bbox_inches='tight')
    print(f"Saved: {out}")


def main():
    plot_scaling()
    print("Done.")


if __name__ == '__main__':
    main()
