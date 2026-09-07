package main

// 项目 27：反射底层——Type 与 Value
//
// reflect 的两根支柱：
//   reflect.Type   —— 类型元数据（字段、tag、方法签名）
//   reflect.Value  —— 运行时的值容器（可读、可写【需可寻址】、可调用）
//
// 演示闭环：
//   1. TypeOf 摸类型 → 遍历字段读名字/类型/tag/偏移
//   2. New(类型) 造实例 → Elem() 拿到可寻址 Value → Set* 写字段
//   3. MethodByName.Call 动态调方法
//   4. 手写 10 行 mini JSON 序列化器，和 encoding/json 对拍
//
// 运行：go run main.go [-json]

import (
	"encoding/json"
	"flag"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

type User struct {
	Name  string   `json:"name"`
	Age   int      `json:"age"`
	Admin bool     `json:"admin"`
	Tags  []string `json:"tags,omitempty"`
	secret string  // 未导出字段：reflect 默认不可见
}

func (u User) FullName(prefix string) string { return prefix + u.Name }

type fieldInfo struct {
	Name   string `json:"name"`
	GoType string `json:"goType"`
	Tag    string `json:"tag"`
	Offset uintptr `json:"offset"`
}

// walkType：TypeOf 遍历字段
func walkType(u User) ([]fieldInfo, string) {
	t := reflect.TypeOf(u)
	desc := fmt.Sprintf("%s, Kind=%v, %d 字段", t.Name(), t.Kind(), t.NumField())
	var fields []fieldInfo
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fields = append(fields, fieldInfo{
			Name: f.Name, GoType: f.Type.String(),
			Tag: string(f.Tag), Offset: f.Offset,
		})
	}
	return fields, desc
}

// reflectNew：New(类型) → 可寻址 → Set
func reflectNew() reflect.Value {
	rv := reflect.New(reflect.TypeOf(User{})) // 指向新实例的指针 Value
	elem := rv.Elem()                         // 可寻址的 struct Value
	elem.FieldByName("Name").SetString("Ada")
	elem.FieldByName("Age").SetInt(36)
	elem.FieldByName("Admin").SetBool(true)
	elem.FieldByName("Tags").Set(reflect.ValueOf([]string{"go", "sys"}))
	return elem
}

// reflectCall：MethodByName.Call 动态调用
func reflectCall(elem reflect.Value) string {
	out := elem.MethodByName("FullName").Call(
		[]reflect.Value{reflect.ValueOf("Dr. ")})
	return out[0].String()
}

// miniJSON：10 行手写反射序列化器（支持 int/string/bool/[]string，tag 改名）
func miniJSON(v reflect.Value) string {
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	t := v.Type()
	var sb strings.Builder
	sb.WriteByte('{')
	first := true
	for i := 0; i < v.NumField(); i++ {
		f := t.Field(i)
		fv := v.Field(i)
		if fv.CanInterface() == false {
			continue
		}
		key := f.Name
		if tag, ok := f.Tag.Lookup("json"); ok {
			key = strings.Split(tag, ",")[0]
		}
		if !first {
			sb.WriteByte(',')
		}
		first = false
		sb.WriteString(`"` + key + `":`)
		switch fv.Kind() {
		case reflect.String:
			sb.WriteString(strconv.Quote(fv.String()))
		case reflect.Int:
			sb.WriteString(strconv.Itoa(int(fv.Int())))
		case reflect.Bool:
			sb.WriteString(strconv.FormatBool(fv.Bool()))
		case reflect.Slice:
			sb.WriteByte('[')
			for j := 0; j < fv.Len(); j++ {
				if j > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString(strconv.Quote(fv.Index(j).String()))
			}
			sb.WriteByte(']')
		}
	}
	sb.WriteByte('}')
	return sb.String()
}

func run() {
	u := User{Name: "Ada", Age: 36, Admin: true, Tags: []string{"go", "sys"}}
	fields, desc := walkType(u)
	elem := reflectNew()
	callRes := reflectCall(elem)
	mini := miniJSON(elem)
	std, _ := json.Marshal(elem.Interface())

	res := map[string]interface{}{
		"typeDesc":   desc,
		"fields":     fields,
		"methodCall": callRes,
		"miniJSON":   mini,
		"stdJSON":    string(std),
		"same":       mini == string(std),
	}
	if !silent {
		pr("== TypeOf ===============\n%s\n", desc)
		for _, f := range fields {
			pr("  %-8s %-10s tag=%-18s off=%d\n",
				f.Name, f.GoType, f.Tag, f.Offset)
		}
		pr("  未导出字段 secret：CanInterface=false，反射看不到\n\n")
		pr("== New+Set（可寻址写字段）=====\n")
		pr("  Name=Ada Age=36 Admin=true Tags=[go sys]（用 reflect 写入）\n\n")
		pr("== MethodByName.Call =====\n  %s\n\n", callRes)
		pr("== miniJSON vs encoding/json =====\n  mini: %s\n  std : %s\n",
			mini, std)
		pr("  一致：%t\n", mini == string(std))
		b, _ := json.MarshalIndent(res, "", "  ")
		pr("\n--- JSON 快照 ---\n%s\n", b)
	} else {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
	}
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		silent = true
	}
	run()
}