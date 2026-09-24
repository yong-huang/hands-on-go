// 实验 03：数组、切片与 map
// 演示：数组定长值拷贝 vs 切片动态共享底层数组、make 的 len/cap、append 扩容、
// map 的增删改查与逗号 ok 惯用法；最后实现验收程序——单词频率统计。
package main

import (
	"fmt"
	"sort"
	"strings"
)

func section(name string) { fmt.Printf("\n========== %s ==========\n", name) }

func main() {
	// ---------- 1. 数组：定长 + 值类型（赋值即整体拷贝） ----------
	section("1. 数组：定长值类型")
	var arr [3]int            // 零值数组：[0 0 0]
	arr[0], arr[2] = 1, 3     // 下标访问，越界在编译期可查出
	copyArr := arr            // 值拷贝：整个数组被复制一份
	copyArr[0] = 99           // 改副本不影响原数组
	fmt.Printf("arr      = %v (len=%d)\n", arr, len(arr))
	fmt.Printf("copyArr  = %v | 修改副本后 arr[0] 仍是 %d\n", copyArr, arr[0])
	fmt.Printf("&arr[0] == &copyArr[0] ? %v\n", &arr[0] == &copyArr[0]) // false：两块独立内存

	// ---------- 2. 切片：动态 + 引用语义（共享底层数组） ----------
	section("2. 切片：动态引用")
	mk := make([]int, 3, 5)                    // make 显式指定 len=3, cap=5
	fmt.Printf("make([]int,3,5) -> len=%d cap=%d %v\n", len(mk), cap(mk), mk)
	s := []int{1, 2, 3}                        // 字面量创建：len=3, cap=3
	alias := s                                 // 切片赋值只复制"头"（指针/len/cap）
	alias[0] = 100                             // 两个变量看到的是同一个底层数组
	fmt.Printf("s=%v alias=%v | 同底层数组: %v\n", s, alias, &s[0] == &alias[0])

	// ---------- 3. append：容量不够就扩容搬家 ----------
	section("3. append 与扩容")
	s = append(s, 4)                           // cap 3 不够，触发扩容（翻倍策略，cap 3→6）
	fmt.Printf("append 4 -> len=%d cap=%d (扩容：底层数组搬家)\n", len(s), cap(s))
	s = append(s, 5)                           // len 追平 cap
	fmt.Printf("append 5 -> len=%d cap=%d (恰好满)\n", len(s), cap(s))
	s = append(s, 6)                           // len 追平 cap，下次 append 才会再扩容
	fmt.Printf("append 6 -> len=%d cap=%d (恰好满)\n", len(s), cap(s))

	// ---------- 4. map：增删改查与逗号 ok ----------
	section("4. map：键值容器")
	ages := map[string]int{"alice": 30}        // 字面量创建
	ages["bob"] = 25                           // 增/改（同一个语法）
	ages["alice"] = 31                         // 改
	v, ok := ages["alice"]                     // 查：comma-ok 惯用法
	fmt.Printf("ages[alice]=%d ok=%v\n", v, ok)
	if _, ok := ages["carol"]; !ok {           // 键不存在时零值+ok=false
		fmt.Println("ages[carol] 不存在, ok=false（零值不报错，所以要靠 ok 区分）")
	}
	delete(ages, "bob")                        // 删：delete 内建函数，键不存在也安全
	_, ok = ages["bob"]
	fmt.Printf("delete 后 ages[bob] ok=%v | len(ages)=%d\n", ok, len(ages))

	// ---------- 5. 验收：单词频率统计 ----------
	section("5. 词频统计（验收）")
	text := "go is simple go is powerful and simple go go"
	words := strings.Fields(text)              // 按空白切出切片
	counts := make(map[string]int)             // 词 -> 次数
	for _, w := range words {                  // 若键不存在，counts[w] 得零值 0，再 +1
		counts[w]++
	}
	keys := make([]string, 0, len(counts))     // 收集键做排序，保证输出稳定
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("文本共 %d 个单词，%d 个不同单词\n", len(words), len(counts))
	for _, k := range keys {
		fmt.Printf("%-9s x%d\n", k, counts[k])
	}
}
