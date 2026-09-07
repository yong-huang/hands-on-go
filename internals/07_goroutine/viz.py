#!/usr/bin/env python3
"""07_goroutine 数据图：go run main.go -json 生成状态快照，画 2 张 PNG 到 images/。

  1. images/growth.png   —— 深递归时 StackInuse 台阶式增长 = goroutine 栈翻倍扩容
  2. images/isolation.png —— N 个并发深递归的栈总量 ≈ 基线 + N × 单栈（线性叠加）

用法：python3 viz.py
"""

import json
import subprocess
from pathlib import Path

import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
from matplotlib.patches import Rectangle

plt.rcParams['font.sans-serif'] = ['PingFang SC', 'Hiragino Sans GB',
                                   'Arial Unicode MS', 'Heiti SC', 'sans-serif']
plt.rcParams['axes.unicode_minus'] = False

HERE = Path(__file__).parent


def load_data():
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


# ---------------------------------------------------------------------------
# 1. growth：StackInuse 台阶增长 = 栈扩容
# ---------------------------------------------------------------------------
def fig_growth(data):
    g = data['growth']
    depths = [x['depth'] for x in g]
    kbs = [x['kb'] for x in g]
    fig, ax = plt.subplots(figsize=(13, 6.2))
    ax.set_xlim(-20, 360)
    ax.set_ylim(0, max(kbs) * 1.25)
    ax.set_title('深递归压栈 → goroutine 栈自动翻倍扩容（StackInuse = 栈内存总量）',
                 fontsize=14, fontweight='bold', pad=18)
    ax.set_xlabel('递归深度（每层在栈上压 4KB 局部数组）', fontsize=11)
    ax.set_ylabel('StackInuse (KB)', fontsize=11)

    colors = ['#2e7d32' if kbs[i] > (kbs[i - 1] if i > 0 else 0) else '#90a4ae'
              for i in range(len(kbs))]
    bars = ax.bar(depths, kbs, width=56, color=colors, edgecolor='#444', lw=1.2,
                  alpha=0.92)
    for b, kb, d in zip(bars, kbs, depths):
        ax.text(b.get_x() + b.get_width() / 2, kb + 60, f'{kb} KB',
                ha='center', fontsize=11, fontweight='bold')

    # 扩容标注
    for i in range(1, len(kbs)):
        if kbs[i] > kbs[i - 1]:
            x = depths[i]
            ax.annotate(f'扩容\n+{kbs[i]-kbs[i-1]} KB', xy=(x, kbs[i]),
                        xytext=(x + 70, kbs[i] + 140),
                        fontsize=10, color='#c62828', fontweight='bold',
                        ha='center',
                        arrowprops=dict(arrowstyle='-|>', color='#c62828', lw=2))
    ax.axhline(kbs[0], color='#888', ls='--', lw=1.2)
    ax.text(6, kbs[0] + 45, f'初始 ~{kbs[0]} KB（初始栈仅 2KB，实测含运行时开销）',
            fontsize=9.5, color='#666')

    ax.text(0.02, 0.02,
            '涨不动时（2304 KB 平台期）说明栈已够用，扩容停止——扩容只发生在"不够"的那一刻',
            transform=ax.transAxes, fontsize=10, color='#555')
    fig.tight_layout()
    fig.savefig(HERE / 'images' / 'growth.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


# ---------------------------------------------------------------------------
# 2. isolation：N 个并发 goroutine 的栈总量线性叠加
# ---------------------------------------------------------------------------
def fig_isolation(data):
    base = data['baseline']
    gs = data['goroutines']
    labels = ['基线'] + [f'{x["n"]} 个并发' for x in gs]
    vals = [base] + [x['kb'] for x in gs]

    fig, ax = plt.subplots(figsize=(13, 6.2))
    ax.set_xlim(-0.7, len(vals) - 0.3)
    ax.set_ylim(0, max(vals) * 1.25)
    ax.set_title('多个 goroutine 并发深递归：每个各占一段独立栈，总量线性叠加',
                 fontsize=14, fontweight='bold', pad=18)
    ax.set_ylabel('StackInuse (KB)', fontsize=11)
    ax.set_xticks(range(len(vals)))
    ax.set_xticklabels(labels, fontsize=11)

    colors = ['#90a4ae'] + ['#1a5fb4'] * len(gs)
    bars = ax.bar(range(len(vals)), vals, width=0.55, color=colors,
                  edgecolor='#333', lw=1.2, alpha=0.92)
    for b, v in zip(bars, vals):
        ax.text(b.get_x() + b.get_width() / 2, v + 60, f'{v} KB',
                ha='center', fontsize=11, fontweight='bold')

    # 线性参考线：基线 + n × 单栈增量
    if gs:
        per = (gs[0]['kb'] - base)  # 1 个 goroutine 的增量
        xs = [0] + [x['n'] for x in gs]
        ys = [base] + [base + n * per for n in xs[1:]]
        ax.plot(xs, ys, ls='--', color='#c62828', lw=2)
        ax.text(0.4, base + per * len(gs) * 0.72,
                f'参考线：基线 + N×{per} KB\n（每 +1 个并发 goroutine ≈ +{per} KB 独立栈）',
                fontsize=10.5, color='#c62828', fontweight='bold')

    fig.tight_layout()
    fig.savefig(HERE / 'images' / 'isolation.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    data = load_data()
    fig_growth(data)
    fig_isolation(data)
    print('已生成 images/growth.png / images/isolation.png')


if __name__ == '__main__':
    main()
