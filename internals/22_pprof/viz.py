#!/usr/bin/env python3
"""22_pprof 可视化：go run main.go -json 取实测数据，画 1 张数据图。

  pprof_speed.png —— 优化闭环的复测对比：hotCPU vs hotCPUfast（ns/op 柱状图）

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


def fig_speed(data):
    fig, ax = plt.subplots(figsize=(8, 5))
    slow = data['cpuSlowNs']
    fast = data['cpuFastNs']
    bars = ax.bar(['hotCPU\n逐字节', 'hotCPUfast\n按字 8B'],
                  [slow, fast], width=0.5, color=['#ef5350', '#26a69a'],
                  edgecolor='#333')
    for b, v in zip(bars, [slow, fast]):
        ax.text(b.get_x() + b.get_width() / 2, v + slow * 0.012,
                f'{v:,} ns/op', ha='center', fontsize=10, fontweight='bold')
    ax.annotate(f'加速 {data["speedup"]:.1f}x：'
                '同样的校验和，循环减少 8 倍',
                xy=(1, fast), xytext=(0.25, slow * 0.62),
                fontsize=10.5, color='#1a5fb4', fontweight='bold',
                arrowprops=dict(arrowstyle='->', color='#1a5fb4'))
    ax.set_ylabel('ns/op（越低越好）', fontsize=11)
    ax.set_title('找到热点（-top flat 最大的函数）→ 只改它 → 复测',
                 fontsize=13, fontweight='bold')
    ax.grid(axis='y', alpha=0.3, ls='--')
    ax.set_ylim(0, slow * 1.18)
    fig.subplots_adjust(left=0.12, right=0.97, top=0.9, bottom=0.12)
    fig.savefig(IMAGES / 'pprof_speed.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    IMAGES.mkdir(exist_ok=True)
    data = load_data()
    fig_speed(data)
    print('已生成 images/pprof_speed.png')


if __name__ == '__main__':
    main()
