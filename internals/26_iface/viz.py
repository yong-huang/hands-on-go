#!/usr/bin/env python3
"""26_iface 可视化：go run main.go -json + objdump 反汇编，2 张 PNG（输出到 images/）。

  1. iface_dispatch.png —— 五种调用方式 ns/op 对照（实测数据，matplotlib）
  2. iface_asm.png      —— 直接调用 vs 接口调用的汇编差异（文本对照面板）

用法：python3 viz.py
"""

import json
import subprocess
import os
import tempfile
from pathlib import Path

import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

plt.rcParams['font.sans-serif'] = ['PingFang SC', 'Hiragino Sans GB',
                                   'Arial Unicode MS', 'Heiti SC', 'sans-serif']
plt.rcParams['axes.unicode_minus'] = False

HERE = Path(__file__).parent
IMAGES = HERE / 'images'
BIN = os.path.join(tempfile.gettempdir(), 'iface_demo')


def load_data():
    out = subprocess.run(['go', 'run', 'main.go', '-json'],
                         cwd=HERE, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


def fig_dispatch(data):
    labels = ['direct\n(内联)', 'direct\n(noinline)', 'interface\n(itab 派发)',
              'type-assert\n+直接', 'type-switch']
    vals = [data['direct'], data['directNI'], data['iface'], data['assert'],
            data['tswitch']]
    colors = ['#26a69a', '#80cbc4', '#c62828', '#ef9a9a', '#ffcc80']

    fig, ax = plt.subplots(figsize=(10, 5))
    bars = ax.bar(labels, vals, width=0.62, color=colors, edgecolor='#333')
    for b, v in zip(bars, vals):
        ax.text(b.get_x() + b.get_width() / 2, v + 0.02, f'{v:.2f}',
                ha='center', fontsize=10.5, fontweight='bold')
    ax.set_ylabel('ns/op（越低越好）', fontsize=11)
    top = max(max(vals), data['directConst'], data['ifaceConst']) * 1.35
    ax.set_ylim(0, top)
    ax.set_title('紧循环里五种调用：派发本身只差 ~1.0x —— 贵的是挡住优化',
                 fontsize=13, fontweight='bold')
    ax.grid(axis='y', alpha=0.3, ls='--')
    # 注：常数方法放大对照
    ax.annotate('「挡死优化」才贵：direct const 编译器删掉整个循环\n'
                f'（{data["directConst"]:.2f}）vs 走接口每次真调'
                f'（{data["ifaceConst"]:.2f}），差 {data["ratioConst"]:.1f}x',
                xy=(2, data['iface']), xytext=(0.9, top * 0.92), fontsize=10,
                color='#b71c1c',
                arrowprops=dict(arrowstyle='->', color='#b71c1c'))
    fig.subplots_adjust(left=0.09, right=0.97, top=0.88, bottom=0.13)
    fig.savefig(IMAGES / 'iface_dispatch.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def build_and_objdump():
    env = dict(os.environ, GO111MODULE='off')
    subprocess.run(['go', 'build', '-o', BIN, 'main.go'], cwd=HERE,
                   env=env, check=True, capture_output=True)

    def disasm(sym, n=12, skip=0):
        out = subprocess.run(
            ['go', 'tool', 'objdump', '-s', sym, BIN],
            capture_output=True, text=True).stdout.splitlines()
        rows = []
        for ln in out:
            s = ln.split('\t')[-1].strip()
            if not s or s.startswith('TEXT') or s.startswith('?'):
                continue
            rows.append(s)
        return rows[skip:skip + n]

    def calls(sym, n=10):
        out = subprocess.run(
            ['go', 'tool', 'objdump', '-s', sym, BIN],
            capture_output=True, text=True).stdout.splitlines()
        rows = [ln.split('\t')[-1].strip() for ln in out if 'CALL' in ln]
        return rows[:n] or ['(no CALL)']

    func1 = disasm(r'main\.run\.func1$')          # direct 闭包：Calc 内联
    func3 = disasm(r'main\.run\.func3$', skip=6)  # iface 闭包：跳过栈帧序言
    runcalls = calls(r'main\.run$')
    return func1, func3, runcalls


def fig_asm():
    func1, func3, runcalls = build_and_objdump()

    fig, ax = plt.subplots(figsize=(13, 4.6))
    ax.axis('off')
    ax.set_title('汇编对照（go tool objdump，go1.26.5/arm64）：接口调用点被 devirtualization 改写',
                 fontsize=13, fontweight='bold')
    panels = [
        ('direct 闭包 run.func1：Calc 内联后只剩读写 sink', func1),
        ('run() 的 CALL 现场：只有 CALL main.bench 与 runtime 调用', runcalls),
        ('iface 闭包 run.func3：itab 比对守卫 + 内联方法体', func3),
    ]
    for col, (title, rows) in enumerate(panels):
        ax.text(0.03 + col * 0.34, 0.93, title, fontsize=10,
                fontweight='bold', color='#1a5fb4',
                transform=ax.transAxes)
        text = '\n'.join(rows) if rows else '(no output)'
        ax.text(0.03 + col * 0.34, 0.86, text, va='top', fontsize=8,
                family='Menlo', color='#333', transform=ax.transAxes,
                linespacing=1.6)
    ax.text(0.03, 0.03,
            'Calc/ConstVal 符号已被内联删除（objdump 查不到）；教科书路径 itab.fun[i] → CALL AX 在本实验被 '
            'devirtualization 消成 itab 比对守卫（CMP 静态 itab，失配才 panicdottypeI）——多实现/跨函数接口仍走真派发。',
            fontsize=9, color='#555', transform=ax.transAxes)
    fig.subplots_adjust(left=0.02, right=0.98, top=0.9, bottom=0.06)
    fig.savefig(IMAGES / 'iface_asm.png', dpi=150, bbox_inches='tight')
    plt.close(fig)


def main():
    data = load_data()
    print('本次绘图使用的实测数据（ns/op）：')
    print(json.dumps(data, indent=2, ensure_ascii=False))
    fig_dispatch(data)
    fig_asm()
    print('已生成 images/iface_dispatch.png / images/iface_asm.png')


if __name__ == '__main__':
    main()
