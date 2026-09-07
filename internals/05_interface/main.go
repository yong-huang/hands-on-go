package main

// 项目 5：Interface 底层——iface 与 eface
//
// 接口变量在内存里是两个字：
//   eface（空接口 interface{}）       : { _type 指针, data 指针 }   —— 动态类型 + 数据
//   iface（带方法的接口）             : { itab 指针, data 指针 }    —— 接口表 + 数据
// itab = { 接口类型, 具体类型, 方法表 }，同一 (接口, 具体类型) 组合全局唯一。
//
// 说明：接口的这两字布局虽是内部实现，但已稳定十余年且属承重结构，
// 可直接用 unsafe 读出来观察；但我们不解引用 _type/itab 的内部字段
// （不同版本字段名可能变），类型名/方法集交给 reflect 这个官方 API。
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 状态快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"reflect"
	"unsafe"
)

// ---------- 演示用的具体类型 ----------

type Point struct{ X, Y int }

type Dog struct{}

func (Dog) Speak() string { return "汪" }
func (Dog) Bite()         {}

type Cat struct{}

func (Cat) Speak() string { return "喵" }

type Speaker interface{ Speak() string }

// ---------- 读接口变量的两个字 ----------

// efaceWords 把 interface{} 当作 {_type, data} 两个指针字读出来
func efaceWords(v interface{}) (typeAddr, dataAddr uintptr) {
	p := (*[2]uintptr)(unsafe.Pointer(&v))
	return p[0], p[1]
}

// ifaceWords 把带方法的接口当作 {itab, data} 两个指针字读出来
func ifaceWords(v Speaker) (itabAddr, dataAddr uintptr) {
	p := (*[2]uintptr)(unsafe.Pointer(&v))
	return p[0], p[1]
}

// ---------- 第 1 节：eface 的结构 ----------

func section1() {
	fmt.Println("========== 1. 空接口 interface{} = { _type, data } ==========")
	n := 42
	s := "hello"
	p := &Point{1, 2}

	e1 := interface{}(n)
	e2 := interface{}(s)
	e3 := interface{}(p)
	e4 := interface{}(99) // 与 e1 同类型 int 的不同值

	show := func(name string, v interface{}) {
		t, d := efaceWords(v)
		rt := reflect.TypeOf(v)
		fmt.Printf("  %-4s %-10s type=0x%x data=0x%x\n",
			name, fmt.Sprintf("%v=%v", rt, v), t, d)
	}
	show("e1", e1)
	show("e2", e2)
	show("e3", e3)
	show("e4", e4)
	fmt.Println("  对比：e1/e4 同是 int → _type 地址相同；e1/e2/e3 类型不同 → _type 地址不同")
	fmt.Println("  结构：eface = { _type 指针(是什么类型), data 指针(数据在哪) }")
}

// ---------- 第 2 节：data 指针的装箱语义 ----------

func section2() {
	fmt.Println("\n========== 2. data 指针：值类型会被装箱拷贝 ==========")
	x := 5
	ex := interface{}(x)
	_, d := efaceWords(ex)
	fmt.Printf("  x  地址 = 0x%x\n", &x)
	fmt.Printf("  ex.data = 0x%x\n", d)
	fmt.Printf("  两者不同 → interface 存的是 x 的一份拷贝（装箱），改 x 不影响 ex\n")
	x = 100
	fmt.Printf("  改 x=100 后 ex=%v（仍为 5，不受影响）\n", ex)

	yp := &x
	ey := interface{}(yp)
	_, d2 := efaceWords(ey)
	fmt.Printf("\n  而指针类型：yp=0x%x, ey.data=0x%x → 直接存指针本身，不再拷贝\n", yp, d2)
	fmt.Println("  → data 指向「装箱」出来的对象；指针/引用类型则 data 就是原指针")
}

// ---------- 第 3 节：iface 的结构 ----------

func section3() {
	fmt.Println("\n========== 3. 带方法接口 = { itab, data } ==========")
	var s1 Speaker = Dog{}
	var s2 Speaker = Cat{}
	var s3 Speaker = Dog{} // 与 s1 同为 Dog

	show := func(name string, v Speaker) {
		itab, d := ifaceWords(v)
		rt := reflect.TypeOf(v)
		ms := make([]string, 0, rt.NumMethod())
		for i := 0; i < rt.NumMethod(); i++ {
			ms = append(ms, rt.Method(i).Name)
		}
		fmt.Printf("  %-3s %-8s itab=0x%x data=0x%x 方法集=%v\n",
			name, rt, itab, d, ms)
	}
	show("s1", s1)
	show("s2", s2)
	show("s3", s3)
	fmt.Println("  对比：s1/s3 同为 Dog → itab 地址相同（itab 全局唯一缓存）")
	fmt.Println("        s2 是 Cat   → itab 地址不同（动态类型不同）")
	fmt.Println("  结构：iface = { itab{接口类型+具体类型+方法表}, data }")
}

// ---------- 第 4 节：类型断言 ----------

func section4() {
	fmt.Println("\n========== 4. 类型断言：检查 _type/itab 是否匹配 ==========")
	var e interface{} = 42

	if v, ok := e.(int); ok {
		fmt.Printf("  e.(int)     → %v, ok=true  （_type 匹配 int）\n", v)
	}
	if _, ok := e.(string); !ok {
		fmt.Println("  e.(string)  → ok=false（_type 不匹配，返回零值）")
	}
	fmt.Println("  原理：断言时拿 eface._type 与目标类型比较，一致才取 data")

	var s Speaker = Dog{}
	switch v := s.(type) {
	case Dog:
		fmt.Printf("  type switch → Dog{}，调用 Speak(): %q\n", v.Speak())
	}
}

// ---------- 第 5 节：nil 陷阱 ----------

func section5() {
	fmt.Println("\n========== 5. nil 陷阱：装了 nil 指针的接口 != nil ==========")
	var p *Dog = nil
	var e interface{} = p
	t, d := efaceWords(e)
	fmt.Printf("  var p *Dog = nil; var e interface{} = p\n")
	fmt.Printf("  e == nil ? %v\n", e == nil)
	fmt.Printf("  eface._type=0x%x  data=0x%x\n", t, d)
	fmt.Printf("  → _type 非空（类型信息还在），所以 e != nil；只有 _type 和 data 都为 0\n")
	var e2 interface{}
	t2, d2 := efaceWords(e2)
	fmt.Printf("  真正 nil 的接口：type=0x%x data=0x%x\n", t2, d2)
}

// ---------- 总结 ----------

func summary() {
	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. interface{}  = { _type, data }，共 16 字节")
	fmt.Println("2. 带方法接口    = { itab, data }；itab 按 (接口,具体类型) 全局唯一")
	fmt.Println("3. 值类型装箱：data 指向拷贝；指针类型：data 就是原指针")
	fmt.Println("4. 类型断言 = 比较 _type/itab；type switch 是它的语法糖")
	fmt.Println("5. 接口存了 nil 指针也不等于 nil：_type 非空")
}

// ---------- JSON 状态快照 ----------

type efaceItem struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Kind     string `json:"kind"`
	Value    string `json:"value"`
	TypeAddr string `json:"type_addr"`
	DataAddr string `json:"data_addr"`
}

func efaceSnapshot() []efaceItem {
	n, s, p, m := 42, "hello", &Point{1, 2}, 99
	items := []efaceItem{
		item(n, "e1 (int 42)"),
		item(s, "e2 (string)"),
		item(p, "e3 (*Point)"),
		item(m, "e4 (int 99)"),
	}
	return items
}

func item(v interface{}, name string) efaceItem {
	t, d := efaceWords(v)
	rt := reflect.TypeOf(v)
	return efaceItem{
		Name: name, Type: rt.String(), Kind: rt.Kind().String(),
		Value: fmt.Sprintf("%v", v), TypeAddr: fmt.Sprintf("0x%x", t),
		DataAddr: fmt.Sprintf("0x%x", d),
	}
}

func ifaceSnapshot() map[string]interface{} {
	var s1, s2, s3 Speaker = Dog{}, Cat{}, Dog{}
	itab1, d1 := ifaceWords(s1)
	itab2, d2 := ifaceWords(s2)
	itab3, d3 := ifaceWords(s3)
	rt := reflect.TypeOf(s1)
	ms := make([]string, 0, rt.NumMethod())
	for i := 0; i < rt.NumMethod(); i++ {
		ms = append(ms, rt.Method(i).Name)
	}
	x := 5
	ex := interface{}(x)
	_, dx := efaceWords(ex)
	yp := &x
	_, dy := efaceWords(interface{}(yp))
	var pnil *Dog
	var enil interface{} = pnil
	tnil, dnil := efaceWords(enil)
	var e2 interface{}
	te2, de2 := efaceWords(e2)
	return map[string]interface{}{
		"iface": []map[string]interface{}{
			{"name": "s1 Dog", "type": "main.Dog", "itab": fmt.Sprintf("0x%x", itab1), "data": fmt.Sprintf("0x%x", d1), "methods": ms},
			{"name": "s2 Cat", "type": "main.Cat", "itab": fmt.Sprintf("0x%x", itab2), "data": fmt.Sprintf("0x%x", d2), "methods": []string{"Speak"}},
			{"name": "s3 Dog", "type": "main.Dog", "itab": fmt.Sprintf("0x%x", itab3), "data": fmt.Sprintf("0x%x", d3), "methods": ms},
		},
		"boxing": []map[string]interface{}{
			{"desc": "int 装箱", "var_addr": fmt.Sprintf("0x%x", &x), "data": fmt.Sprintf("0x%x", dx)},
			{"desc": "指针直存", "var_addr": fmt.Sprintf("0x%x", yp), "data": fmt.Sprintf("0x%x", dy)},
		},
		"nil_pitfall": map[string]interface{}{
			"desc":       "interface{}((*Dog)(nil))",
			"type_addr":  fmt.Sprintf("0x%x", tnil),
			"data_addr":  fmt.Sprintf("0x%x", dnil),
			"eq_nil":     false,
			"true_nil_t": fmt.Sprintf("0x%x", te2),
			"true_nil_d": fmt.Sprintf("0x%x", de2),
		},
	}
}

func jsonOut() {
	out := map[string]interface{}{
		"eface": efaceSnapshot(),
		"iface": ifaceSnapshot(),
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		jsonOut()
		return
	}
	section1()
	section2()
	section3()
	section4()
	section5()
	summary()
}
