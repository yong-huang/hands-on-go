#!/usr/bin/env python3
"""19_syscall 可视化：go run main.go -json 生成快照，画 2 张 PNG。

  1. syscall_threads.png —— 各阶段 G 数 / M 数（线程）随阻塞进程的变化
  2. syscall_waiting.png —— 卡锁 worker 数与新增线程数对照

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
PHASE_FULL = {'baseline': '基线', 'start': '启动',
              'blocked-8': '8 全卡 lock', 'blocked+': '持续阻塞',
              'after-release': '释放锁'}


def load_data():
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


def fig_threads(data):
    phases = [PHASE_FULL.get(s['phase'], s['phase']) for s in data]
    gs = [s['goroutines'] for s in data]
    ts = [s['threads'] for s in data]

    fig, ax = plt.subplots(figsize=(10, 5.2))
    xs = range(len(data))
    ax.plot(xs, ts, 'o-', color='#c62828', lw=2.4, ms=8, label='M（OS 线程, 累计）')
    ax.plot(xs, gs, 's--', color='#1a5fb4', lw=2, ms=7, label='G（goroutine）')
    ax.axhline(2, color='#e66700', ls=':', lw=1.5)
    ax.text(len(data) - 0.4, 2.3, 'GOMAXPROCS = 2', color='#e66700', fontsize=9)
    for i, s in enumerate(data):
        if s['phase'].startswith('blocked'):
            ax.annotate(f"+{s['threadDiff']} 线程 (阻塞新建)",
                        (i, s['threads']), textcoords='offset points',
                        xytext=(6, 10), fontsize=9, color='#c62828',
                        fontweight='bold')
    ax.set_xticks(list(xs))
    ax.set_xticklabels(phases, fontsize=10)
    ax.set_ylabel('数量', fontsize=11)
    ax.set_ylim(0, max(gs + ts) + 4)
    ax.set_title('真实阻塞 syscall（flock）时 M 暴增，超过 GOMAXPROCS',
                 fontsize=13, fontweight='bold')
    ax.grid(alpha=0.3, ls='--')
    ax.legend(fontsize=9.5, loc='upper left')
    fig.subplots_adjust(left=0.09, right=0.97, top=0.9, bottom=0.13)
    fig.savefig(IMAGES / 'syscall_threads.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def fig_waiting(data):
    phases = [PHASE_FULL.get(s['phase'], s['phase']) for s in data]
    waits = [s['waiting'] for s in data]
    diffs = [s['threadDiff'] for s in data]

    fig, ax = plt.subplots(figsize=(10, 4.9))
    xs = range(len(data))
    b1 = ax.bar(xs, waits, width=0.5, color='#f6c177', edgecolor='#a05a00',
                label='卡在 flock 的 worker')
    b2 = ax.bar([x + 0.3 for x in xs], diffs, width=0.5, color='#c62828',
                edgecolor='#7a1515', label='新增 M 线程数')
    for (i, w), (j, d) in zip(enumerate(waits), enumerate(diffs)):
        ax.text(i - 0.15, w + 0.15, str(w), ha='center', fontsize=9)
        ax.text(i + 0.15, d + 0.15, f'+{d}', ha='center', fontsize=9,
                color='#c62828')
    ax.set_xticks(list(xs))
    ax.set_xticklabels(phases, fontsize=10)
    ax.set_ylabel('数量', fontsize=11)
    ax.set_ylim(0, max(waits + diffs) + 2.2)
    ax.set_title('每个躺在 syscall 里的 G 都会占掉一个 M，直到锁释放',
                 fontsize=13, fontweight='bold')
    ax.grid(axis='y', alpha=0.3, ls='--')
    ax.legend(fontsize=9.5)
    fig.subplots_adjust(left=0.09, right=0.97, top=0.88, bottom=0.13)
    fig.savefig(IMAGES / 'syscall_waiting.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    data = load_data()
    fig_threads(data)
    fig_waiting(data)
    print('已生成 syscall_threads.png / syscall_waiting.png')


if __name__ == '__main__':
    main()