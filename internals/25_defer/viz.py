#!/usr/bin/env python3
"""25_defer 数据图：go run main.go -json，画 defer vs 手动 Unlock 的 ns/op 对照。

机制图（执行顺序）已改为 Archify 时序图 images/order.sequence.json，
本脚本只负责实测数据图 —— Archify 画不了实测数据，保留 matplotlib。

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


def fig_perf(data):
    fig, ax = plt.subplots(figsize=(8, 5))
    d = data['deferNs']
    nd = data['nodeferNs']
    bars = ax.bar(['useDefer\n（defer mu.Unlock）', 'noDefer\n（手动 Unlock）'],
                  [d, nd], width=0.5, color=['#ef9a9a', '#a5d6a7'],
                  edgecolor='#333')
    for b, v in zip(bars, [d, nd]):
        ax.text(b.get_x() + b.get_width() / 2, v + 0.12, f'{v:.2f} ns/op',
                ha='center', fontsize=11, fontweight='bold')
    ax.annotate(f'defer 额外开销 {data["ratio"]:.2f}x\n'
                '（open-coded defer，编译期展开）',
                xy=(0, d), xytext=(0.3, d + 2.0), fontsize=10.5,
                color='#c62828', fontweight='bold',
                arrowprops=dict(arrowstyle='->', color='#c62828'))
    ax.text(0.45, nd - 2.6, '收益：异常路径也自动解锁\n（正确性 > 几纳秒）',
            fontsize=9.5, color='#2e7d32')
    ax.set_ylabel('ns/op（越低越好）', fontsize=11)
    ax.set_ylim(0, d * 1.6)
    ax.set_title(f'defer 不是洪水猛兽：{data["ratio"]:.2f}x 换"永远记得解锁"',
                 fontsize=13, fontweight='bold')
    ax.grid(axis='y', alpha=0.3, ls='--')
    fig.subplots_adjust(left=0.11, right=0.97, top=0.9, bottom=0.12)
    fig.savefig(IMAGES / 'defer_perf.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    IMAGES.mkdir(exist_ok=True)
    data = load_data()
    fig_perf(data)
    print('已生成 images/defer_perf.png')


if __name__ == '__main__':
    main()
