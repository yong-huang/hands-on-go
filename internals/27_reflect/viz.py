#!/usr/bin/env python3
"""27_reflect 数据图：go run main.go -json，画实测对照图。

  reflect_json.png  —— 字段/tag 清单 + miniJSON vs encoding/json 对拍

（机制图已由 Archify 三件套 images/flow.{workflow.json,html,svg} 承接）

用法：python3 viz.py   # 输出到 images/
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
OUT = HERE / 'images'


def load_data():
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


def fig_json(data):
    fields = data['fields']
    OUT.mkdir(exist_ok=True)
    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(12, 5))

    ax1.axis('off')
    ax1.set_title('字段清单：名字 / 类型 / tag / 偏移', fontsize=12.5,
                  fontweight='bold')
    rows = [[f['name'], f['goType'], f['tag'], f['offset']] for f in fields]
    col = ['字段', 'Go 类型', 'tag', '偏移']
    tbl = ax1.table(cellText=rows, colLabels=col, loc='center',
                    cellLoc='left', colWidths=[0.16, 0.2, 0.42, 0.16])
    tbl.auto_set_font_size(False)
    tbl.set_fontsize(9)
    tbl.scale(1, 1.8)
    for j in range(4):
        tbl[0, j].set_facecolor('#bbdefb')
    tbl[4, 0].set_facecolor('#ef9a9a')  # secret 高亮

    ax2.axis('off')
    ax2.set_title('miniJSON（10 行手写反射） vs encoding/json',
                  fontsize=12.5, fontweight='bold')
    ax2.text(0.03, 0.72, 'mini: ' + data['miniJSON'], fontsize=9,
             family='Menlo', transform=ax2.transAxes, wrap=True)
    ax2.text(0.03, 0.5, 'std : ' + data['stdJSON'], fontsize=9,
             family='Menlo', transform=ax2.transAxes, wrap=True)
    ax2.text(0.03, 0.32, f'输出完全一致 = {data["same"]}', fontsize=11,
             color='#2e7d32', fontweight='bold', transform=ax2.transAxes)
    ax2.text(0.03, 0.14, 'encoding/json 内部就是这么走的：反射读 tag → 反射写输出；'
                         '代价是慢一个数量级', fontsize=9, color='#555',
             transform=ax2.transAxes)

    fig.suptitle('反射三件套：读元数据、动态建/写、动态调',
                 fontsize=13.5, fontweight='bold', y=1.0)
    fig.tight_layout()
    fig.savefig(OUT / 'reflect_json.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    data = load_data()
    fig_json(data)
    print('已生成 images/reflect_json.png')


if __name__ == '__main__':
    main()
