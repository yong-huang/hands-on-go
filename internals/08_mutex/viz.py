#!/usr/bin/env python3
"""08_mutex 数据图：go run main.go -json 生成状态快照，画 2 张 PNG 到 images/。

  1. images/spin.png       —— 竞争者 waiters 在几微秒内爬满（自旋窗口极短）
  2. images/starvation.png —— 持续竞争下 starving 位随时间的切换

（state 位布局机制图已由架构图三件套 images/state.svg / state.html 承担）

用法：python3 viz.py
"""

import subprocess
from pathlib import Path

import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

plt.rcParams['font.sans-serif'] = ['PingFang SC', 'Hiragino Sans GB',
                                   'Arial Unicode MS', 'Heiti SC', 'sans-serif']
plt.rcParams['axes.unicode_minus'] = False

HERE = Path(__file__).parent


def load_data():
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    import json
    return json.loads(out.stdout)


# ---------------------------------------------------------------------------
# 1. 自旋窗口
# ---------------------------------------------------------------------------
def fig_spin(data):
    ramp = data['spin']
    ts = [(i + 1) * 2 for i in range(len(ramp))]
    fig, ax = plt.subplots(figsize=(13, 5.6))
    ax.set_xlim(-1, 14)
    ax.set_ylim(0, max(ramp) * 1.35)
    ax.set_title('竞争者抢锁：waiters 在几微秒内爬满（自旋窗口极短）',
                 fontsize=14, fontweight='bold', pad=18)
    ax.set_xlabel('持锁时间（微秒）', fontsize=11)
    ax.set_ylabel('waiters（排队人数）', fontsize=11)

    ax.step(ts, ramp, where='post', color='#1a5fb4', lw=2.5)
    ax.scatter(ts, ramp, s=70, color='#c62828', zorder=5)
    for t, w in zip(ts, ramp):
        ax.text(t, w + max(ramp) * 0.04, str(w), ha='center', fontsize=11,
                fontweight='bold')

    ax.axvspan(0, 3, color='#d5f5e3', alpha=0.6)
    ax.text(1.5, max(ramp) * 1.15, '自旋窗口（约 2 微秒）', ha='center', fontsize=10,
            color='#2e7d32', fontweight='bold')
    ax.text(6.5, max(ramp) * 0.5, '自旋失败 → 立刻 park 入队', fontsize=10,
            color='#c62828', fontweight='bold')
    ax.text(-0.9, max(ramp) * 0.08,
            'Go 互斥锁自旋只有几次迭代：宁可快速入队，也不长空转浪费 CPU',
            fontsize=9.5, color='#666')
    fig.tight_layout()
    fig.savefig(HERE / 'images' / 'spin.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


# ---------------------------------------------------------------------------
# 2. 饥饿模式切换
# ---------------------------------------------------------------------------
def fig_starvation(data):
    st = data['starvation']
    ts = [s['t_us'] / 1000 for s in st]  # ms
    starve = [1.0 if s['starving'] else 0.0 for s in st]
    waiters = [s['waiters'] for s in st]

    fig, ax = plt.subplots(figsize=(13, 5.8))
    ax.set_xlim(0, max(ts) * 1.08)
    ax.set_ylim(-0.15, 2.6)
    ax.set_title('持续竞争：入队者累计等待 >1ms → starving 位置位，反复进出饥饿模式',
                 fontsize=14, fontweight='bold', pad=18)
    ax.set_xlabel('时间 (ms)', fontsize=11)

    ax.step(ts, starve, where='post', color='#c62828', lw=3)
    ax.fill_between(ts, 0, starve, step='post', color='#ffd6d6', alpha=0.8)
    ax.set_yticks([0, 1])
    ax.set_yticklabels(['正常模式', '饥饿模式'], fontsize=10)

    first = next((i for i, s in enumerate(st) if s['starving']), None)
    if first is not None:
        ax.annotate(f'首次进入饥饿\n(t≈{ts[first]:.1f}ms)', xy=(ts[first], 1),
                    xytext=(ts[first] - 3.5, 2.15), fontsize=10, color='#c62828',
                    fontweight='bold', ha='center',
                    arrowprops=dict(arrowstyle='-|>', color='#c62828', lw=2))

    ax.text(0.02, 0.3, f'waiters 全程 ≈ {max(waiters)}（红色高亮段 = 饥饿模式，FIFO 直通）',
            transform=ax.transAxes, fontsize=10, color='#555')
    fig.tight_layout()
    fig.savefig(HERE / 'images' / 'starvation.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    data = load_data()
    fig_spin(data)
    fig_starvation(data)
    print('已生成 images/spin.png / images/starvation.png')


if __name__ == '__main__':
    main()
