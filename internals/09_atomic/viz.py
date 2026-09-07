#!/usr/bin/env python3
"""09_atomic 数据图：go run main.go -json 生成数据，画 1 张 PNG 到 images/。

  1. images/atomic_vs_plain.png —— 并发计数：期望 / 非原子(丢更新) / 原子(精确)

（CAS 重试循环与 release/acquire 屏障机制图已由 Archify 图
  images/cas.svg / cas.html 与 images/barrier.svg / barrier.html 承担）

用法：python3 viz.py
"""

import json
import subprocess
from pathlib import Path

import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

plt.rcParams['font.sans-serif'] = ['PingFang SC', 'Hiragino Sans GB',
                                   'Arial Unicode MS', 'Heiti SC', 'sans-serif']
plt.rcParams['axes.unicode_minus'] = False

HERE = Path(__file__).parent


def load_data():
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


# ---------------------------------------------------------------------------
# 1. 原子 vs 非原子（实测数据柱状图）
# ---------------------------------------------------------------------------
def fig_count(data):
    e = data['exp1']
    fig, ax = plt.subplots(figsize=(13, 5.8))
    ax.set_xlim(0, 3.6)
    ax.set_ylim(-e['expected'] * 0.35, e['expected'] * 1.32)

    bars = [
        ('期望值\n%d' % e['expected'], e['expected'], '#90a4ae'),
        ('非原子 c++\n%d（丢 %d）' % (e['non_atomic'], e['lost']),
         e['non_atomic'], '#c62828'),
        ('原子 AddInt64\n%d（精确）' % e['atomic'], e['atomic'], '#2e7d32'),
    ]
    for i, (label, val, c) in enumerate(bars):
        x = i + 0.6
        ax.bar(x, val, width=0.55, color=c, edgecolor='#333')
        ax.text(x, val + e['expected'] * 0.03, f'{val:,}',
                ha='center', fontsize=11, fontweight='bold')
        ax.text(x, -e['expected'] * 0.09, label, ha='center', fontsize=10.5)

    lost_pct = e['lost'] / e['expected'] * 100
    ax.annotate('', xy=(1.85, e['expected']), xytext=(1.85, e['non_atomic']),
                arrowprops=dict(arrowstyle='-|>', color='#c62828', lw=3))
    ax.text(2.1, (e['expected'] + e['non_atomic']) / 2,
            f'丢失 %.0f%% 的更新' % lost_pct, color='#c62828', fontsize=11,
            fontweight='bold', va='center')
    ax.text(0.6, e['expected'] * 1.18,
            '16 个 goroutine 各 +100000 次；并发 c++ 是读-改-写三步，互相覆盖 → 丢更新',
            fontsize=10.5, color='#333')
    ax.axis('off')
    fig.savefig(HERE / 'images' / 'atomic_vs_plain.png', dpi=150,
                bbox_inches='tight')
    plt.close(fig)


def main():
    data = load_data()
    fig_count(data)
    print('已生成 images/atomic_vs_plain.png')


if __name__ == '__main__':
    main()
