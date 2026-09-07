#!/usr/bin/env python3
"""02_map 可视化：go run main.go -json 生成状态快照，画桶数组占用 PNG。

  occupancy.png —— 桶数组占用条（两场景并排）：实测数据图，
  展示"大多数桶空闲，冲突 key 挤在同一个桶 → 溢出链"的负载分布。

用法：python3 viz.py
"""

import json
import os
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
    env = dict(os.environ)
    env['GO111MODULE'] = 'off'
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True, env=env)
    return json.loads(out.stdout)


def fig_occupancy(data):
    fig, axes = plt.subplots(1, 2, figsize=(11, 3.4))
    titles = ['m2：插入 15 个 key（B=1，扩容后 B=2）', 'm3：冲突演示（B=4，16 桶）']
    for ax, key, title in zip(axes, ['m2', 'm3'], titles):
        st = data[key]
        n = st['buckets']
        occ = st['occupied']
        colors = ['#d64545' if i == st['busiest'] else '#2e8b57' for i in range(n)]
        ax.bar(range(n), occ, color=colors, edgecolor='#333', linewidth=0.8)
        ax.set_xticks(range(n))
        ax.set_xlabel('桶下标')
        ax.set_ylabel('元素数（含溢出链）')
        ax.set_title(f"{title}\ncount={st['count']}  B={st['b']}  负载={st['load']:.2f}（阈值 6.5）",
                     fontsize=10)
        if key == 'm3':
            ax.annotate(f"冲突桶 #{st['busiest']}", xy=(st['busiest'], occ[st['busiest']]),
                        xytext=(st['busiest'] + 1.6, occ[st['busiest']] + 0.4),
                        fontsize=9, color='#c01c28',
                        arrowprops=dict(arrowstyle='->', color='#c01c28'))
    fig.suptitle('桶数组占用情况：大多数桶空闲，冲突 key 挤在同一个桶 → 溢出链',
                 fontsize=13, fontweight='bold')
    fig.tight_layout(rect=(0, 0, 1, 0.9))
    fig.savefig(IMAGES / 'occupancy.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    IMAGES.mkdir(exist_ok=True)
    data = load_data()
    fig_occupancy(data)
    print('已生成 images/occupancy.png')


if __name__ == '__main__':
    main()
