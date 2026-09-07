#!/usr/bin/env python3
"""16_alloc_bench 可视化：go run main.go -json 生成快照，画数据图 alloc_bench.png。

  alloc_bench —— 三组对照的 ns/op（log 轴）与 allocs/op 双栏实测对比

用法：python3 viz.py   # 输出到 images/alloc_bench.png
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


def load_data():
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


def fmt_ns(v):
    if v >= 1000:
        return f'{v/1000:.0f}us'
    if v >= 100:
        return f'{v:.0f}'
    return f'{v:.1f}'


def fig(data):
    bench = data['bench']
    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(13, 5.6),
                                   gridspec_kw={'wspace': 0.35})
    labels = []
    ns = []
    allocs = []
    for _, b in enumerate(bench):
        labels.append(b['name'])
        ns.append(b['ns'])
        allocs.append(b['allocs'])

    # 左：ns/op（log 轴）
    colors = ['#1a5fb4', '#4aa3df',   # 组1
              '#e65100', '#ffa726',   # 组2
              '#2e7d32', '#a5d6a7']   # 组3
    bars = ax1.bar(range(len(labels)), ns, color=colors, edgecolor='#444')
    ax1.set_xticks(range(len(labels)))
    ax1.set_xticklabels(labels, fontsize=9, rotation=24, ha='right')
    ax1.set_ylabel('ns/op（每对左为新写法/右为对照）', fontsize=10)
    ax1.set_title('耗时对比', fontsize=13, fontweight='bold')

    # 右：allocs/op
    bars2 = ax2.bar(range(len(allocs)), allocs, color=colors, edgecolor='#444')
    ax2.set_xticks(range(len(allocs)))
    ax2.set_xticklabels(labels, fontsize=9, rotation=24, ha='right')
    ax2.set_ylabel('allocs/op', fontsize=10)
    ax2.set_title('堆分配次数对比', fontsize=13, fontweight='bold')
    for bb, a in zip(bars2, allocs):
        ax2.text(bb.get_x() + bb.get_width() / 2, a + 0.3, f'{a:.0f}',
                 ha='center', fontsize=9, fontweight='bold')

    ax1.annotate('', xy=(1.5, max(ns) * 1.0), xytext=(1.5, max(ns)),
                 arrowprops=dict(arrowstyle='-', color='#fff'))
    ax2.text(0.02, 0.9, 'Pool 复用 0 allocs；预分配 2 vs 扩容 10',
             transform=ax2.transAxes, fontsize=9, color='#333')

    for i in range(0, len(labels), 2):
        ax1.annotate('', xy=(i + 1, ns[i + 1] * 1.25), xytext=(i, ns[i + 1] * 1.25),
                     color='#888', arrowprops=dict(arrowstyle='-', color='#888', lw=0.8))

    fig.suptitle('对象分配调优：写法与策略的实测代价', fontsize=14, fontweight='bold')
    fig.subplots_adjust(top=0.85, left=0.07, right=0.98, bottom=0.16)
    fig.savefig(IMAGES / 'alloc_bench.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    data = load_data()
    IMAGES.mkdir(exist_ok=True)
    fig(data)
    print('已生成 images/alloc_bench.png')


if __name__ == '__main__':
    main()