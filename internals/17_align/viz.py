#!/usr/bin/env python3
"""17_align 可视化：go run main.go -json 生成快照，画数据图 PNG。

  align_sizes.png —— 三个结构体的大小构成（字段内容 vs 填充）实测对比柱状图
  （机制图 layout 已改为 Archify 交互图，见 images/layout.architecture.json）

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


def fig_sizes(data):
    structs = data['structs']
    names = [s['name'].split('(')[0] for s in structs]
    sizes = [int(s['size']) for s in structs]
    contents = [sum(int(f['size']) for f in s['fields']) for s in structs]
    pads = [s - c for s, c in zip(sizes, contents)]

    fig, ax = plt.subplots(figsize=(10, 5.4))
    xs = range(len(structs))
    b1 = ax.bar(xs, contents, width=0.5, color='#1a5fb4', edgecolor='#444',
                label='字段内容')
    b2 = ax.bar(xs, pads, width=0.5, bottom=contents, color='#bdbdbd',
                edgecolor='#444', label='填充（padding）')
    for x, c, p, s in zip(xs, contents, pads, sizes):
        ax.text(x, s / 2, f'{c}B 内容', ha='center', fontsize=9, color='white',
                fontweight='bold')
        ax.text(x, c + p / 2, f'{p}B', ha='center', fontsize=8.5, color='#555')
        ax.text(x, s + 0.6, f'{s} B', ha='center', fontsize=10.5, va='bottom',
                fontweight='bold', color='#c62828')
    ax.set_xticks(list(xs))
    ax.set_xticklabels(names, fontsize=10)
    ax.set_ylabel('字节数', fontsize=11)
    ax.set_ylim(0, max(sizes) * 1.25)
    ax.set_title('badOrder 24B vs goodOrder 16B：只是把 8B 字段移到前面',
                 fontsize=13, fontweight='bold')
    ax.legend(fontsize=9)
    fig.subplots_adjust(left=0.08, right=0.98, top=0.9, bottom=0.12)
    fig.savefig(IMAGES / 'align_sizes.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    IMAGES.mkdir(exist_ok=True)
    data = load_data()
    fig_sizes(data)
    print('已生成 images/align_sizes.png')


if __name__ == '__main__':
    main()
