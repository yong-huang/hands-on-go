#!/usr/bin/env python3
"""13_escape 可视化：go run main.go -json 生成状态快照，画实测数据图 PNG。

  1. images/alloc_delta.png —— 每个场景调用 20 万次的 TotalAlloc 增量（log 轴）

（机制图已改由 Archify 图管线绘制：images/escape.* 三件套）

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
IMAGES = HERE / 'images'

C_HEAP = '#c62828'
C_STACK = '#2e7d32'


def load():
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


def fmt_bytes(n):
    if n >= 1e9:
        return f'{n/1e9:.1f} GB'
    if n >= 1e6:
        return f'{n/1e6:.1f} MB'
    if n >= 1e3:
        return f'{n/1e3:.0f} KB'
    return f'{n} B'


def fig_delta(data):
    ds = data['deltas']
    names = [d['func'] for d in ds]
    vals = [d['alloc_delta'] for d in ds]
    colors = [C_HEAP if d['heap'] else C_STACK for d in ds]

    fig, ax = plt.subplots(figsize=(13, 5.6))
    bars = ax.bar(range(len(ds)), vals, color=colors, edgecolor='#444')
    ax.set_xticks(range(len(ds)))
    ax.set_xticklabels(names, fontsize=9.5, rotation=22, ha='right')
    ax.set_yscale('log')
    ax.set_ylim(1, max(vals) * 10)
    ax.set_yticks([1, 1e3, 1e6, 1e9, 1e10, 1e13])
    ax.set_yticklabels(['1B', '1KB', '1MB', '1GB', '10GB', '10TB'], fontsize=9)
    ax.set_ylabel('TotalAlloc 增量（调用 20 万次）', fontsize=11)
    ax.set_title('逃逸场景每次调用都要堆分配 → TotalAlloc 线性上涨；栈上场景为 0',
                 fontsize=13, fontweight='bold', pad=16)
    for b, v in zip(bars, vals):
        if v == 0:
            ax.text(b.get_x() + b.get_width() / 2, 1.6, '0', ha='center',
                    fontsize=10, color=C_STACK, fontweight='bold')
        else:
            ax.text(b.get_x() + b.get_width() / 2, v * 1.8, fmt_bytes(v),
                    ha='center', fontsize=8.5, color='#444')
    ax.axhline(1, color='#aaa', ls='--', lw=1)
    ax.text(0.99, 3.2, '栈上 = 不产生任何分配', transform=ax.transAxes, fontsize=9,
            color=C_STACK)

    fig.subplots_adjust(left=0.08, right=0.98, top=0.9, bottom=0.16)
    IMAGES.mkdir(exist_ok=True)
    fig.savefig(IMAGES / 'alloc_delta.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    data = load()
    fig_delta(data)
    print('已生成 images/alloc_delta.png')


if __name__ == '__main__':
    main()
