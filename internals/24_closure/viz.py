#!/usr/bin/env python3
"""24_closure 数据可视化：go run main.go -json 实测地址分布，1 张 PNG。

  images/closure_addr.png —— 循环捕获（共享 &i）vs 参数传值（各持一份）的地址散点

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


def fig_addr(data):
    loop = data['loopAddrs']
    fixed = data['fixedAddrs']

    fig, ax = plt.subplots(figsize=(9, 5))
    ax.scatter([0] * len(loop), loop, s=90, c='#c62828', zorder=3,
               edgecolors='#7a1515', label='循环捕获（共享 &i）')
    ax.scatter([1] * len(fixed), fixed, s=90, c='#1a5fb4', zorder=3,
               edgecolors='#0c3a75', label='参数传值（各持一份）')
    ax.set_xticks([0, 1])
    ax.set_xticklabels(['循环变量声明在循环外\n所有 goroutine 指向同一 &i',
                        '传参修复\n每个 goroutine 拿自己的 j'], fontsize=10.5)
    ax.set_ylabel('捕获变量地址', fontsize=11)
    mid_loop = loop[0]
    ax.annotate(f'唯一地址 {data["loopUniqueAddrs"]} 个（5 点重合）',
                (0, mid_loop), xytext=(0.1, mid_loop + 300000),
                fontsize=10, color='#c62828', fontweight='bold',
                arrowprops=dict(arrowstyle='->', color='#c62828'))
    ax.annotate(f'唯一地址 {data["fixedUniqueAddrs"]} 个',
                (1, fixed[0]), xytext=(0.62, mid_loop - 500000),
                fontsize=10, color='#1a5fb4', fontweight='bold',
                arrowprops=dict(arrowstyle='->', color='#1a5fb4'))
    ax.set_title(f'循环捕获 5 份地址全相同、读到 {data["loopValues"]}（终止值） vs '
                 f'传参各自一份、读到 {data["fixedValues"]}（正确）',
                 fontsize=12.5, fontweight='bold')
    pad = max(max(fixed) - mid_loop, mid_loop - min(fixed)) * 1.6 + 100000
    ax.set_ylim(mid_loop - pad, mid_loop + pad)
    ax.yaxis.set_major_formatter(plt.FuncFormatter(lambda v, _: f'0x…{int(v) % 0x10000:04x}'))
    ax.grid(alpha=0.3, ls='--')
    ax.legend(fontsize=9.5, loc='lower center')
    fig.subplots_adjust(left=0.14, right=0.97, top=0.88, bottom=0.16)
    fig.savefig(IMAGES / 'closure_addr.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    IMAGES.mkdir(exist_ok=True)
    data = load_data()
    fig_addr(data)
    print('已生成 images/closure_addr.png')


if __name__ == '__main__':
    main()
