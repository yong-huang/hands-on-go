"""Worker Pool 数据图：串行 vs 池化耗时对比 + 20 任务在 5 worker 上的调度甘特图

数据来源：worker_pool.go 的真实参数 —— 任务 i 耗时 (50+i*10) ms，5 workers，20 tasks。
"""

import os
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
import numpy as np

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
IMG_DIR = os.path.join(SCRIPT_DIR, 'images')

# macOS 中文字体，避免 DejaVu Sans 缺字
matplotlib.rcParams['font.sans-serif'] = ['PingFang SC', 'Hiragino Sans GB', 'Arial Unicode MS']
matplotlib.rcParams['axes.unicode_minus'] = False

NUM_WORKERS = 5
NUM_TASKS = 20


def task_duration(i):
    """与 worker_pool.go demo 一致：task i 睡 (50+i*10) ms。"""
    return 50 + i * 10


def plot_scaling():
    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(14, 6))

    # ---- 左图：串行 vs Worker Pool（由真实任务耗时模型推得） ----
    serial_ms = sum(task_duration(i) for i in range(1, NUM_TASKS + 1))
    # 贪心调度模拟：任务按序提交，先空闲的 worker 领取下一个任务
    free_at = [0.0] * NUM_WORKERS
    gantt = []  # (worker, task, start, dur)
    for i in range(1, NUM_TASKS + 1):
        w = int(np.argmin(free_at))
        start = free_at[w]
        dur = task_duration(i)
        gantt.append((w, i, start, dur))
        free_at[w] = start + dur
    pool_ms = max(free_at)

    bars = ax1.bar(['串行执行', 'Worker Pool\n(5 workers)'],
                   [serial_ms, pool_ms],
                   color=['#c44e52', '#4c72b0'], width=0.55)
    for b, v in zip(bars, [serial_ms, pool_ms]):
        ax1.text(b.get_x() + b.get_width() / 2, v + 40, f'{v:.0f} ms',
                 ha='center', fontsize=11, fontweight='bold')
    ax1.set_ylabel('总耗时 (ms)')
    ax1.set_title(f'{NUM_TASKS} 个任务：串行 vs 池化（go run 实测约 601ms，量级一致）')
    ax1.grid(axis='y', alpha=0.3)
    ax1.set_ylim(0, serial_ms * 1.12)

    # ---- 右图：任务 → worker 调度甘特图 ----
    colors = ['#4c72b0', '#55a868', '#c44e52', '#8172b2', '#ccb974']
    for w, i, start, dur in gantt:
        ax2.barh(w, dur, left=start, height=0.6, color=colors[w],
                 edgecolor='white', alpha=0.9)
        ax2.text(start + dur / 2, w, f'T{i}', ha='center', va='center',
                 fontsize=7, color='white', fontweight='bold')

    ax2.set_xlabel('时间 (ms)')
    ax2.set_ylabel('Worker ID')
    ax2.set_title(f'任务调度甘特图（{NUM_WORKERS} Workers, {NUM_TASKS} Tasks）')
    ax2.set_yticks(range(NUM_WORKERS))
    ax2.set_yticklabels([f'W{w + 1}' for w in range(NUM_WORKERS)])
    ax2.invert_yaxis()
    ax2.grid(axis='x', alpha=0.3)

    fig.tight_layout()
    out = os.path.join(IMG_DIR, 'worker_pool_scaling.png')
    fig.savefig(out, dpi=150, bbox_inches='tight')
    print(f"Saved: images/worker_pool_scaling.png  (serial={serial_ms}ms, pool={pool_ms:.0f}ms)")


def main():
    os.makedirs(IMG_DIR, exist_ok=True)
    plot_scaling()
    print("Done.")


if __name__ == '__main__':
    main()
