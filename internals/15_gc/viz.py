#!/usr/bin/env python3
"""15_gc 可视化：go run main.go -json 生成状态快照，画实测数据图 PNG。

  1. images/gc_pause.png —— 真实 GC 停顿（ms）与堆对象回收量

（机制图已改由图源管线绘制：images/tricolor.* 三件套）

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


def load_data():
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


# ---------------------------------------------------------------------------
# 真实 GC 停顿与堆回收
# ---------------------------------------------------------------------------
def fig_pause(data):
    gc = data['gc']
    pauses = gc['pauses_ms']

    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(13, 5.4),
                                   gridspec_kw={'wspace': 0.32})
    ax1.set_title('每轮 runtime.GC() 停顿（STW）', fontsize=13, fontweight='bold')
    xs = list(range(1, len(pauses) + 1))
    bars = ax1.bar(xs, pauses, width=0.55, color='#c62828', edgecolor='#444')
    for b, p in zip(bars, pauses):
        ax1.text(b.get_x() + b.get_width() / 2, p + 0.01, f'{p:.2f}',
                 ha='center', fontsize=9.5, fontweight='bold')
    ax1.set_xticks(xs)
    ax1.set_xticklabels([f'第{k}轮' for k in xs], fontsize=9)
    ax1.set_ylabel('停顿 (ms)', fontsize=10)
    ax1.set_ylim(0, max(pauses) * 1.35)
    ax1.text(0.02, 0.9, '停顿是 STW 标记开销；图每两轮减半，停顿随之回落',
             transform=ax1.transAxes, fontsize=8.5, color='#666')

    ax2.set_title('清扫回收：堆对象数', fontsize=13, fontweight='bold')
    counts = [gc['heap_objects_before'], gc['heap_objects_after']]
    bars = ax2.bar(['GC 前', '多轮 GC 后'], counts, width=0.45,
                   color=['#546e7a', '#2e7d32'], edgecolor='#444')
    for b, c in zip(bars, counts):
        ax2.text(b.get_x() + b.get_width() / 2, c * 1.03,
                 f'{c:,}', ha='center', fontsize=10, fontweight='bold')
    ax2.set_ylabel('HeapObjects', fontsize=10)
    ax2.set_ylim(0, counts[0] * 1.3)
    ax2.text(0.02, 0.9, '白色对象全部被清扫回收\n（200 万节点图归零）',
             transform=ax2.transAxes, fontsize=8.5, color='#666')

    fig.subplots_adjust(left=0.07, right=0.98, top=0.88, bottom=0.12)
    IMAGES.mkdir(exist_ok=True)
    fig.savefig(IMAGES / 'gc_pause.png', dpi=150, bbox_inches='tight')
    plt.close(fig)

def main():
    data = load_data()
    fig_pause(data)
    print('已生成 images/gc_pause.png')


if __name__ == '__main__':
    main()
