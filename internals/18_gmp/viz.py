#!/usr/bin/env python3
"""18_gmp 可视化：go run main.go -json 生成快照，画实测数据图。

  gmp_stats.png —— 三种取任务来源的数量/占比对比（真实运行数据）

机制图（GMP 结构与调度流程）已改用架构图三件套：images/gmp.html / gmp.svg。

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
SRC_COLOR = {'local': '#1a5fb4', 'global': '#e66100', 'steal': '#c62828'}


def load_data():
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


def fig_stats(data):
    by = data['bySrc']
    labels = ['local\n（局部性）', 'global\n（公平）', 'steal\n（负载均衡）']
    vals = [by.get('local', 0), by.get('global', 0), by.get('steal', 0)]
    colors = [SRC_COLOR[k] for k in ('local', 'global', 'steal')]
    total = sum(vals)

    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(11, 4.8),
                                   gridspec_kw={'width_ratios': [1.6, 1]})
    bars = ax1.bar(labels, vals, width=0.55, color=colors, edgecolor='#333')
    for b, v in zip(bars, vals):
        ax1.text(b.get_x() + b.get_width() / 2, v + 0.6, f'{v}\n({v / total:.0%})',
                 ha='center', fontsize=10, fontweight='bold')
    ax1.set_ylabel('取任务次数', fontsize=11)
    ax1.set_ylim(0, max(vals) * 1.25)
    ax1.set_title(f'三种取任务来源（共 {total} 次）', fontsize=12, fontweight='bold')
    ax1.grid(axis='y', alpha=0.3, ls='--')

    ax2.pie(vals, labels=[k for k in ('local', 'global', 'steal')],
            colors=colors, autopct='%.0f%%', startangle=90,
            textprops={'fontsize': 10})
    ax2.set_title('来源占比', fontsize=12, fontweight='bold')
    fig.suptitle('不均衡负载（3 个 8 任务 G 挤在 P0）下，steal 帮忙分摊',
                 fontsize=13, fontweight='bold', y=1.03)
    fig.tight_layout()
    IMAGES.mkdir(exist_ok=True)
    fig.savefig(IMAGES / 'gmp_stats.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    data = load_data()
    fig_stats(data)
    print('已生成 images/gmp_stats.png')


if __name__ == '__main__':
    main()
