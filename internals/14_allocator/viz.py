#!/usr/bin/env python3
"""14_allocator 可视化：go run main.go -json 生成状态快照，画实测数据图 PNG。

  1. images/addr_density.png —— tiny/small/large 三类对象的地址分布实证

（机制图已改由 Archify 图管线绘制：images/tiers.* 三件套）

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

C_TINY   = '#8e24aa'
C_SMALL  = '#1a5fb4'
C_LARGE  = '#e65100'


def load_data():
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


# ---------------------------------------------------------------------------
# 地址分布实证（tiny/small/large）
# ---------------------------------------------------------------------------
def fig_addr(data):
    tiny = data['tiny_offsets']
    small_gaps = data['small']['gaps']
    large = data['large']

    fig, axs = plt.subplots(1, 3, figsize=(14.5, 5.2))

    # tiny
    ax = axs[0]
    ax.set_title('tiny：1B 对象偏移（相对首个）', fontsize=12, fontweight='bold')
    ax.scatter(tiny, [0.06] * len(tiny), marker='|', s=260, color=C_TINY, lw=2)
    blocks = sorted(set(o // 16 for o in tiny))
    for b in blocks:
        ax.axvspan(b * 16, b * 16 + 15, color=C_TINY, alpha=0.09)
        ax.text(b * 16 + 7.5, 0.5, f'16B块{b}', fontsize=7.5, color=C_TINY, ha='center')
    ax.set_xlabel('字节偏移', fontsize=9)
    ax.set_ylim(0, 0.85)
    ax.set_yticks([])
    ax.text(0.02, 0.86, f'{len(tiny)} 个对象挤进 {len(blocks)} 个 16B 块',
            transform=ax.transAxes, fontsize=9, color=C_TINY, fontweight='bold')

    # small
    ax = axs[1]
    ax.set_title('small：相邻地址间隔分布', fontsize=12, fontweight='bold')
    ax.hist(small_gaps, bins=30, color=C_SMALL, edgecolor='white')
    ax.set_xlabel('相邻地址间隔 bytes', fontsize=9)
    ax.set_ylabel('频数', fontsize=9)
    pct = sum(1 for g in small_gaps if g == 64) / len(small_gaps) * 100
    ax.text(0.02, 0.9, f'间隔 64B 占 {pct:.0f}%（size class）；HeapInuse +{data["small"]["heap_inuse_delta"]:,} B',
            transform=ax.transAxes, fontsize=8.5, color=C_SMALL, fontweight='bold')

    # large
    ax = axs[2]
    ax.set_title('large：4 个 48KB 对象', fontsize=12, fontweight='bold')
    xs = list(range(1, len(large['deltas']) + 1))
    ax.bar(xs, [d / 1024 for d in large['deltas']], width=0.5,
           color=C_LARGE, edgecolor='#444')
    ax.axhline(48, color='#999', ls='--', lw=1, label='对象大小 48KB')
    ax.set_xticks(xs)
    ax.set_xticklabels([f'gap{i}' for i in xs], fontsize=9)
    ax.set_ylabel('间隔 (KB)', fontsize=9)
    ax.legend(fontsize=8, loc='upper left')
    ax.text(0.02, 0.9, '页内偏移全 0（地址页对齐，独占 span）',
            transform=ax.transAxes, fontsize=8.5, color=C_LARGE, fontweight='bold')

    fig.suptitle('三条分配路径的地址实证', fontsize=14, fontweight='bold')
    fig.subplots_adjust(top=0.82, left=0.06, right=0.98, wspace=0.3)
    IMAGES.mkdir(exist_ok=True)
    fig.savefig(IMAGES / 'addr_density.png', dpi=150, bbox_inches='tight')
    plt.close(fig)

def main():
    data = load_data()
    fig_addr(data)
    print('已生成 images/addr_density.png')


if __name__ == '__main__':
    main()
