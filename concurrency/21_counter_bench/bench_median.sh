#!/bin/sh
# 计数器三路基准 · 中位数统计脚本
# 用法：
#   cd concurrency
#   go test -run '^$' -bench . -benchmem -cpu=1,2,4,8,16 -count=5 \
#     ./21_counter_bench/ | sh 21_counter_bench/bench_median.sh
# 对每行基准结果按 (实现 × 档位) 分组，取 -count=5 的 ns/op 中位数，
# 按实现与档位排序输出矩阵。注：基准不带 -race（数据竞争检测会成倍
# 放大临界区成本，扭曲结论）；-race 只用于正确性测试。
grep '^Benchmark' | awk '
{
    name = $1; ns = $3;
    cnt[name]++; vals[name] = vals[name] " " ns;
}
END {
    for (name in vals) {
        n = split(vals[name], a, " ");
        for (i = 1; i <= n; i++)
            for (j = i + 1; j <= n; j++)
                if (a[j] + 0 < a[i] + 0) { t = a[i]; a[i] = a[j]; a[j] = t }
        printf "%s\t%s\t%d\n", name, a[int((n + 1) / 2)], n;
    }
}' | sort
