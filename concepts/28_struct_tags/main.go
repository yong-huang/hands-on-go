package main

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// 28_struct_tags：Struct Tag 解析
//
// struct tag 是写在字段反引号里的元数据字符串，编译器不解释它，
// 但运行时可以通过 reflect.StructTag 读出来——json/validate/orm 等框架
// 全靠这一层"字符串协议"驱动。本程序演示四件事：
//  1. 用 reflect 读 tag：Lookup vs Get 的关键区别（区分"没写"与"写了空串"）
//  2. json 键名映射：模拟 encoding/json 的"有 tag 用 tag，没 tag 用字段名"
//  3. validate 规则检查：required 非空 / min 最小值，输出校验报告
//  4. miniMarshal：只输出带 json tag 的字段，拼成 JSON 风格字符串
//
// 运行：go run main.go

// User 演示用结构体：required / min / 无 validate 三种 tag 组合
type User struct {
	Name  string `json:"name" validate:"required"`
	Age   int    `json:"age" validate:"min=0"`
	Email string `json:"email"` // 只参与序列化，不参与校验
}

// Demo 演示 Lookup 与 Get 的区别：tag 的三种写法
type Demo struct {
	A string `json:"a"` // 写了 tag，值 "a"
	B string `json:""`  // 写了 tag，但值是空串
	C string // 完全没写 tag
}

// keyOf 返回字段的 json 键名：Lookup 命中且非 "-" 用 tag 值，否则退回字段名
func keyOf(f reflect.StructField) (string, bool) {
	tag, ok := f.Tag.Lookup("json")
	if !ok || tag == "-" {
		return f.Name, false // 没写 json tag 或写了 "-"：序列化器跳过
	}
	name := strings.Split(tag, ",")[0] // "age,omitempty" 只取逗号前的键名
	if name == "" {
		name = f.Name // `json:""` 空键名：退回字段名
	}
	return name, true
}

// validate 按字段 validate tag 逐条检查，返回校验报告（空报告 = 全过）
func validate(v any) []string {
	var report []string
	t := reflect.TypeOf(v)
	val := reflect.ValueOf(v)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		rule, ok := f.Tag.Lookup("validate")
		if !ok {
			continue // 无 validate tag：不校验（这就是 Email 被跳过的原因）
		}
		fv := val.Field(i)
		switch {
		case rule == "required":
			if fv.IsZero() {
				report = append(report, fmt.Sprintf("FAIL %s: required 但为零值", f.Name))
			}
		case strings.HasPrefix(rule, "min="):
			min, _ := strconv.Atoi(strings.TrimPrefix(rule, "min="))
			if fv.Int() < int64(min) {
				report = append(report, fmt.Sprintf("FAIL %s: %d < min=%d", f.Name, fv.Int(), min))
			}
		}
	}
	return report
}

// miniMarshal 只输出带 json tag 的字段，拼成 JSON 风格字符串
func miniMarshal(v any) string {
	t := reflect.TypeOf(v)
	val := reflect.ValueOf(v)
	parts := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		key, ok := keyOf(t.Field(i))
		if !ok {
			continue
		}
		fv := val.Field(i)
		s := fv.String()
		if fv.Kind() != reflect.String {
			s = fmt.Sprint(fv.Interface())
		}
		parts = append(parts, fmt.Sprintf("%q:%q", key, s))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func main() {
	fmt.Println("========== 1. Lookup vs Get：区分\"没写 tag\"和\"写了空 tag\" ==========")
	t := reflect.TypeOf(Demo{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		v, ok := f.Tag.Lookup("json")
		fmt.Printf("%s: Lookup=(%q, %v) | Get=%q\n", f.Name, v, ok, f.Tag.Get("json"))
	}
	fmt.Println("→ Get 把\"没写\"和\"写了空串\"都折叠成 \"\"；Lookup 用 ok 区分两者")

	fmt.Println("\n========== 2. json 键名映射 → 简化序列化 miniMarshal ==========")
	u := User{Name: "Ada", Age: 36, Email: "ada@example.com"}
	fmt.Println("miniMarshal:", miniMarshal(u))
	fmt.Println("Email 有 json tag 所以输出；若写成 `json:\"-\"` 则会被跳过")

	fmt.Println("\n========== 3. validate 规则检查：合法用户 ==========")
	if report := validate(u); len(report) == 0 {
		fmt.Println("校验通过：0 条失败")
	} else {
		fmt.Println(report)
	}

	fmt.Println("\n========== 4. validate：非法用户（Name 空 + Age 为负）==========")
	bad := User{Name: "", Age: -1, Email: "none"}
	fmt.Println("miniMarshal(bad):", miniMarshal(bad))
	for _, line := range validate(bad) {
		fmt.Println(line)
	}

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. tag 是反引号字符串，运行时用 reflect.StructTag 读取")
	fmt.Println("2. Get 合并\"没写\"与\"空串\"；Lookup 的第二个返回值 ok 才是真值表")
	fmt.Println("3. json 键名映射：有 tag 用 tag，`json:\"\"` 或缺失退回字段名，\"-\" 跳过")
	fmt.Println("4. validate/miniMarshal 都是\"读 tag → 按 Kind 分派\"，框架的本质就这一步")
}
