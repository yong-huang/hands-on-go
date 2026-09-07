#!/usr/bin/env python3
"""21_cgo 数据图：跑三种模式（独立进程）取 RSS 快照，画 1 张 PNG。

  cgo_rss.png —— leak / free / go 三模式 分配后 vs GC三遍后 RSS 柱状对比
  （机制图已改为 Archify：images/ownership.architecture.json → svg/html）

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


def run_sample(mode):
    out = subprocess.run(['go', 'run', 'main.go', '-mode', mode, '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


def load_data():
    modes = []
    for m in ('leak', 'free', 'go'):
        modes.append(run_sample(m))
    return modes


def fig_rss(modes):
    labels = ['leak\n（C 泄漏）', 'free\n（C.free）', 'go\n（GC 管）']
    before = [m['rssBefore'] / 1048576 for m in modes]
    after = [m['rssAfter'] / 1048576 for m in modes]
    heap_peak = [m['goHeapMax'] for m in modes]

    fig, ax = plt.subplots(figsize=(9, 5.2))
    xs = range(len(modes))
    b1 = ax.bar([x - 0.17 for x in xs], before, width=0.34,
                color='#ef5350', edgecolor='#333', label='分配后 RSS')
    b2 = ax.bar([x + 0.17 for x in xs], after, width=0.34,
                color='#26a69a', edgecolor='#333', label='GC 三遍后 RSS')
    for x, bf, af in zip(xs, before, after):
        ax.text(x - 0.17, bf + 0.8, f'{bf:.0f}', ha='center', fontsize=9.5)
        ax.text(x + 0.17, af + 0.8, f'{af:.0f}', ha='center', fontsize=9.5)
    for x, hp in zip(xs, heap_peak):
        if hp:
            ax.annotate(f'Go 堆峰值 {hp}MB', (x, 0), xytext=(x - 0.2, 66),
                        fontsize=9, color='#1a5fb4',
                        arrowprops=dict(arrowstyle='->', color='#1a5fb4'))
    baseline = min(before)
    ax.axhline(baseline, color='#888', ls=':', lw=1.2)
    ax.text(2.4, baseline + 1.2, f'基线 RSS ≈ {baseline:.0f}MB',
            fontsize=9, color='#666')
    ax.set_xticks(list(xs))
    ax.set_xticklabels(labels, fontsize=10.5)
    ax.set_ylabel('RSS（MB）', fontsize=11)
    ax.set_ylim(0, 84)
    ax.set_title('64MB 内存的三种话语权：C 泄漏 / C.free / Go GC',
                 fontsize=13, fontweight='bold')
    ax.legend(fontsize=9.5)
    ax.grid(axis='y', alpha=0.3, ls='--')
    fig.subplots_adjust(left=0.09, right=0.97, top=0.9, bottom=0.13)
    fig.savefig(IMAGES / 'cgo_rss.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    IMAGES.mkdir(exist_ok=True)
    modes = load_data()
    fig_rss(modes)
    print('已生成 images/cgo_rss.png')


if __name__ == '__main__':
    main()
