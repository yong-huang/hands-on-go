#!/usr/bin/env python3
"""23_inline 数据图：go run main.go -json 实测，三组 内联 vs noinline 的 ns/op 对照。

  images/inline_ns.png —— 数据图（matplotlib；架构图画不了实测数据）

机制图（内联决策流程）走图源管线，见 images/inline.workflow.json。
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


def fig_ns(data):
    groups = [
        ('smallSum8\n（小函数）', data['smallInlineNs'], data['smallNoinlNs'],
         data['smallRatio']),
        ('bigLoop\n（大函数）', data['bigNs'], data['bigNoinlNs'],
         data['bigRatio']),
        ('constForty\n（常数函数）', data['cfInlineNs'], data['cfNoinlNs'],
         data['cfRatio']),
    ]
    labels = [g[0] for g in groups]
    inl = [g[1] for g in groups]
    ni = [g[2] for g in groups]
    ratios = [g[3] for g in groups]
    xs = range(len(groups))

    fig, ax = plt.subplots(figsize=(9, 5.2))
    w = 0.34
    ax.bar([x - w / 2 for x in xs], inl, w, color='#26a69a', edgecolor='#333',
           label='内联（默认）')
    ax.bar([x + w / 2 for x in xs], ni, w, color='#ef5350', edgecolor='#333',
           label='//go:noinline')
    for x, gi, gn, r in zip(xs, inl, ni, ratios):
        ax.text(x - w / 2, gi + max(inl) * 0.02, f'{gi:.2f}', ha='center',
                fontsize=9, color='#00695c', fontweight='bold')
        ax.text(x + w / 2, gn + max(inl) * 0.02, f'{gn:.2f}', ha='center',
                fontsize=9, color='#b71c1c', fontweight='bold')
        ax.annotate(f'{r:.1f}x', (x + w / 2, gn),
                    xytext=(x - 0.1, max(inl) * 0.55), fontsize=11,
                    color='#1a5fb4', fontweight='bold',
                    arrowprops=dict(arrowstyle='->', color='#1a5fb4'))
    ax.set_xticks(list(xs))
    ax.set_xticklabels(labels, fontsize=10.5)
    ax.set_ylabel('ns/op（越低越好）', fontsize=11)
    ax.set_ylim(0, max(inl) * 1.35)
    ax.set_title('三组对照：call 开销 / 函数体开销 / 常量折叠（数值随机器与运行漂移，看比值）',
                 fontsize=12.5, fontweight='bold')
    ax.legend(fontsize=9.5)
    ax.grid(axis='y', alpha=0.3, ls='--')
    fig.subplots_adjust(left=0.11, right=0.97, top=0.9, bottom=0.12)
    fig.savefig(IMAGES / 'inline_ns.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    data = load_data()
    fig_ns(data)
    print('已生成 images/inline_ns.png')
    print(json.dumps(data, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
