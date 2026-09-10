#!/usr/bin/env python3
"""30_runtime 可视化：go run main.go -json，画 2 张数据 PNG（机制图由架构图三件套承接）。

  1. images/runtime_sched.png —— 4 个 M 的泳道图（run/steal/wait/idle）+ 调度统计
  2. images/runtime_alloc.png —— 分配器/GC 统计柱状图（实测数据）

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
EV_COLOR = {'run': '#4caf50', 'steal': '#ff9800',
            'wait': '#bdbdbd', 'idle': '#ef9a9a'}


def load_data():
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


def fig_sched(data):
    sched = data['scheduler']
    events = data['events']
    t0 = min(e['ts'] for evs in events for e in evs)
    fig, ax = plt.subplots(figsize=(11, 5.2))
    nP = len(events)
    for m in range(nP):
        evs = events[m]
        for i, e in enumerate(evs):
            start = e['ts'] - t0
            end = (evs[i + 1]['ts'] if i + 1 < len(evs) else e['ts'] + 0.05) - t0
            ax.barh(m, end - start, left=start, height=0.65, color=EV_COLOR[e['ev']],
                    edgecolor='#555', linewidth=0.4)
    ax.set_yticks(range(nP))
    ax.set_yticklabels([f'M{m}' for m in range(nP)], fontsize=10)
    ax.set_xlabel('相对时间 (ms)')
    ax.set_title('简化 GMP：4 个 M（真 goroutine）的调度泳道 — '
                 '绿=run 橙=steal 灰=wait 红=idle',
                 fontsize=12.5, fontweight='bold')
    ax.set_xlim(0, None)
    handles = [plt.Rectangle((0, 0), 1, 1, fc=c, ec='#555')
               for c in EV_COLOR.values()]
    ax.legend(handles, ['run（执行 G）', 'steal（工作窃取）',
                        'wait（队列空，等待）', 'idle（全部完成）'],
              fontsize=8.5, ncol=2, loc='lower right')
    ax.text(0.98, 1.06, f'任务={sched["taskCount"]}  完成={sched["done"]}  '
                        f'窃取={sched["steals"]}  本地队列峰值={sched["maxLocalQueue"]}',
            transform=ax.transAxes, ha='right', fontsize=10, color='#1a5fb4',
            fontweight='bold')
    fig.tight_layout()
    fig.savefig(HERE / 'images' / 'runtime_sched.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def fig_alloc(data):
    alloc = data['allocator']
    fig, ax = plt.subplots(figsize=(8, 5))

    cats = ['分配次数\n(allocs)', '回收批数\n(frees)', 'GC 次数\n(gcRuns)',
            '存活字节\n(liveBytes)', '回收字节\n(collected)', '保留根\n(roots)']
    vals = [alloc['allocs'], alloc['frees'], alloc['gcRuns'],
            alloc['liveBytes'], alloc['collectedBytes'], alloc['keptRoots']]
    bars = ax.bar(cats, vals, color=['#90caf9', '#a5d6a7', '#ef9a9a',
                                     '#64b5f6', '#ffcc80', '#e1bee7'],
                  width=0.6)
    for b, v in zip(bars, vals):
        ax.text(b.get_x() + b.get_width() / 2, v + max(vals) * 0.02,
                str(v), ha='center', fontsize=9, fontweight='bold')
    ax.set_ylabel('计数 / 字节')
    ax.set_title('分配器 + GC 总账（分配 1200 次仅 184 个池对象周转，'
                 '3 次 GC 后仅 15 个根对象存活）',
                 fontsize=12, fontweight='bold')
    ax.tick_params(axis='x', labelsize=8)

    fig.tight_layout()
    fig.savefig(HERE / 'images' / 'runtime_alloc.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    data = load_data()
    fig_sched(data)
    fig_alloc(data)
    print('已生成 images/runtime_sched.png / images/runtime_alloc.png')


if __name__ == '__main__':
    main()
