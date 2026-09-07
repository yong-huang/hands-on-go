#!/usr/bin/env python3
"""29_leak 可视化：go run main.go -json，画 2 张 PNG。

  1. leak_goroutine.png —— goroutine 数随阶段变化 + 阻塞点栈（取自 pprof）
  2. leak_memory.png    —— HeapAlloc 随阶段变化（100MB 背景引用现形）

用法：python3 viz.py
"""

import json
import re
import subprocess
from pathlib import Path

import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

plt.rcParams['font.sans-serif'] = ['PingFang SC', 'Hiragino Sans GB',
                                   'Arial Unicode MS', 'Heiti SC', 'sans-serif']
plt.rcParams['axes.unicode_minus'] = False

HERE = Path(__file__).parent
PHASE_COLORS = ['#90caf9', '#ffcc80', '#ef9a9a', '#a5d6a7']


def load_data():
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


def parse_goroutine_prof(path):
    """runtime/pprof 文本格式：'N @ 0x...' + 栈帧行。返回 [(count, [frames])]。"""
    groups = []
    cur = None
    for ln in open(path):
        ln = ln.strip()
        m = re.match(r'^(\d+) @', ln)
        if m:
            if cur:
                groups.append(cur)
            cur = {'count': int(m.group(1)), 'frames': []}
        elif cur and ln:
            ln = re.sub(r'\s+', ' ', ln)          # tab/多空格归一
            fn = ln.split('+')[0].strip()
            fn = re.sub(r'^0x[0-9a-f]+\s*', '', fn)  # 去地址前缀
            cur['frames'].append(fn)
    if cur:
        groups.append(cur)
    return sorted(groups, key=lambda g: -g['count'])


def fig_goroutine(data):
    phases = data['phases']
    labels = [p['label'] for p in phases]
    counts = [p['goroutines'] for p in phases]
    groups = parse_goroutine_prof(data['goroutineProf'])

    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(12, 5))
    bars = ax1.bar(labels, counts, color=PHASE_COLORS, width=0.55)
    for b, c in zip(bars, counts):
        ax1.text(b.get_x() + b.get_width() / 2, c + 0.4, str(c),
                 ha='center', fontsize=11, fontweight='bold')
    ax1.set_ylabel('goroutine 数')
    ax1.set_title('goroutine 泄漏：+20 个永远阻塞的协程', fontsize=12.5,
                  fontweight='bold')
    ax1.set_ylim(0, max(counts) * 1.25)
    ax1.tick_params(axis='x', labelsize=8.5, rotation=12)

    ax2.axis('off')
    ax2.set_title('goroutine profile 揭底：卡在哪（top 栈）', fontsize=12.5,
                  fontweight='bold')
    for i, g in enumerate(groups[:4]):
        ax2.text(0.03, 0.86 - i * 0.2, f'{g["count"]} 个 →',
                 fontsize=10, fontweight='bold', color='#c62828',
                 transform=ax2.transAxes)
        ax2.text(0.2, 0.86 - i * 0.2,
                 ' <- '.join(g['frames'][:3]) or '(runtime 内部)',
                 fontsize=8.5, family='Menlo', color='#333',
                 transform=ax2.transAxes, wrap=True)
    ax2.text(0.03, 0.08,
             '关键：' + groups[0]['frames'][-1] if groups else '',
             fontsize=9, color='#555', transform=ax2.transAxes)

    fig.suptitle('泄漏 1：goroutine 泄漏（chan 永远没人接）',
                 fontsize=13.5, fontweight='bold', y=1.0)
    fig.tight_layout()
    fig.savefig(HERE / 'images' / 'leak_goroutine.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def fig_memory(data):
    phases = data['phases']
    labels = [p['label'] for p in phases]
    alloc = [p['heapAllocMB'] for p in phases]

    fig, ax = plt.subplots(figsize=(10, 5))
    bars = ax.bar(labels, alloc, color=PHASE_COLORS, width=0.55)
    for b, v in zip(bars, alloc):
        ax.text(b.get_x() + b.get_width() / 2, v + 2, f'{v:.1f}MB',
                ha='center', fontsize=10, fontweight='bold')
    ax.set_ylabel('HeapAlloc (MB)')
    ax.set_title('泄漏 2：100MB 大数组被 16 字节小切片"钉"在堆上',
                 fontsize=12.5, fontweight='bold')
    ax.set_ylim(0, max(alloc) * 1.18)
    ax.tick_params(axis='x', labelsize=8.5, rotation=12)
    ax.annotate('GC 后仍 100MB\n（keep 引用钉住底层数组）',
                xy=(2, alloc[2]), xytext=(2.6, max(alloc) * 0.7),
                fontsize=9, color='#c62828',
                arrowprops=dict(arrowstyle='->', color='#c62828'))
    ax.text(0.5, -0.16,
            f'另：泄漏 3 为 5 个未关闭文件（fd），goroutine 数不变、堆不变，'
            f'用 lsof 才能看到（data["leakedFiles"]={data["leakedFiles"]}）',
            transform=ax.transAxes, fontsize=9, color='#555', ha='center')
    fig.tight_layout()
    fig.savefig(HERE / 'images' / 'leak_memory.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    data = load_data()
    fig_goroutine(data)
    fig_memory(data)
    print('已生成 leak_goroutine.png / leak_memory.png')


if __name__ == '__main__':
    main()