#!/usr/bin/env python3
"""28_trace 可视化：go run main.go -json，画 2 张 PNG。

  1. trace_sched.png   —— go tool trace -pprof=sched + pprof -top 调度延迟分解
  2. trace_timeline.png —— 自建埋点画的 goroutine 生命周期（三类任务分泳道）

用法：python3 viz.py
"""

import json
import os
import re
import subprocess
import tempfile
from pathlib import Path

import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

plt.rcParams['font.sans-serif'] = ['PingFang SC', 'Hiragino Sans GB',
                                   'Arial Unicode MS', 'Heiti SC', 'sans-serif']
plt.rcParams['axes.unicode_minus'] = False

HERE = Path(__file__).parent
COLORS = {'cpu': '#1a5fb4', 'io': '#2e7d32', 'chan': '#e65100'}


def load_data():
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


def run_sched_profile(trace_path):
    """go tool trace -pprof=sched 输出去 temp，再 pprof -top 解析。"""
    from subprocess import DEVNULL
    with tempfile.NamedTemporaryFile(suffix='.prof', delete=False) as tf:
        prof = tf.name
    subprocess.run(['go', 'tool', 'trace', '-pprof=sched', trace_path],
                   stdout=open(prof, 'wb'), stderr=DEVNULL, check=True)
    out = subprocess.run(['go', 'tool', 'pprof', '-top', prof],
                         capture_output=True, text=True, check=True).stdout
    os.unlink(prof)
    rows = []
    for ln in out.splitlines():
        m = re.match(r'\s*(\S+)\s+(\S+)%\s+(\S+)%\s+(\S+)\s+(\S+)%\s+(.+)\s*$', ln)
        if not m:
            continue
        flat_us, flat_pct, _, _, _, func = m.groups()
        try:
            flat_val = float(flat_us.rstrip('us'))
            if flat_val <= 0:
                continue
        except ValueError:
            continue
        rows.append((func, flat_val, float(flat_pct)))
    total = sum(r[1] for r in rows)
    total_pct = sum(r[2] for r in rows)
    return rows[:8], total, total_pct


def fig_sched(rows, total, total_pct):
    funcs = [r[0] for r in rows][::-1]
    vals = [r[1] for r in rows][::-1]
    pcts = [r[2] for r in rows][::-1]
    fig, ax = plt.subplots(figsize=(10, 5.2))
    bars = ax.barh(funcs, vals, color='#74c0fc')
    ax.set_xlabel('调度延迟 (μs)')
    ax.set_title(f'标量调度延迟分解：认领延迟总量 {total:.1f}μs（前 8 项占 {total_pct:.0f}%）',
                 fontsize=12.5, fontweight='bold')
    for b, v, p in zip(bars, vals, pcts):
        ax.text(b.get_width() + 1, b.get_y() + b.get_height() / 2,
                f'{v:.1f}μs ({p:.1f}%)', va='center', fontsize=8.5)
    ax.set_xlim(0, max(vals) * 1.3)
    ax.tick_params(labelsize=8.5)
    fig.tight_layout()
    fig.savefig(HERE / 'images' / 'trace_sched.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def fig_timeline(data):
    events = data['events']
    t0 = min(e['tsMs'] for e in events)
    kinds = ['cpu', 'io', 'chan']
    rows = {}  # (kind,id) -> row idx
    n = 0
    spans = {}
    for e in events:
        key = (e['kind'], e['id'])
        if key not in rows:
            rows[key] = n
            n += 1
        if e['phase'] == 'start':
            spans[key] = {'start': e['tsMs'] - t0}
        else:
            spans[key].update({'end': e['tsMs'] - t0})

    fig, ax = plt.subplots(figsize=(11, 6))
    max_end = max(s['end'] for s in spans.values())
    lane_c = {'cpu': '#a5d6a7', 'io': '#90caf9', 'chan': '#ffcc80'}
    for key, s in spans.items():
        kind, gid = key
        y = rows[key]
        ax.barh(y, s['end'] - s['start'], left=s['start'],
                color=lane_c[kind], edgecolor=COLORS[kind], height=0.6)

    # 泳道分隔背景 + 每组（kind）分区
    yticks, ylabs = [], []
    for key, ridx in rows.items():
        yticks.append(ridx)
        ylabs.append(f'{key[0]}-g{key[1]}')
    ax.set_yticks(yticks)
    ax.set_yticklabels(ylabs, fontsize=8)

    for i in range(n):
        ax.axhspan(i - 0.5, i + 0.5, color='#f8f8f8' if i % 2 else 'white',
                   zorder=0)

    for kind in kinds:
        ids = [ridx for key, ridx in rows.items() if key[0] == kind]
        if len(ids) > 1:
            ax.axhline(min(ids) + len(ids) - 0.5, color=COLORS[kind],
                       lw=2, alpha=0.6)

    ax.set_xlim(0, max_end * 1.1)
    ax.set_xlabel('相对时间 (ms)')
    ax.set_title('goroutine 生命周期（自建埋点）：CPU 密集 / I-O 阻塞 / 管道交接',
                 fontsize=12.5, fontweight='bold')
    handles = [plt.Rectangle((0, 0), 1, 1, fc=COLORS[k], ec=COLORS[k], label={
        'cpu': 'CPU 密集（忙循环，占住 P）',
        'io': 'I/O 阻塞（让出 P）',
        'chan': 'channel 交接（chan 队列暂停）'}[k]) for k in kinds]
    ax.legend(handles=handles, fontsize=9, loc='upper right')
    fig.tight_layout()
    fig.savefig(HERE / 'images' / 'trace_timeline.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    data = load_data()
    rows, total, total_pct = run_sched_profile(data['tracePath'])
    fig_sched(rows, total, total_pct)
    fig_timeline(data)
    print(f'已生成 trace_sched.png / trace_timeline.png'
          f'（调度延迟总量 {total:.0f}μs）')


if __name__ == '__main__':
    main()