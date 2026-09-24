// 30_custom_marshal：自定义 JSON 序列化。
// CustomTime 通过实现 json.Marshaler / json.Unmarshaler 接口，
// 把 time.Time 的默认 RFC3339 格式替换为 "2006-01-02 15:04:05"。
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// layout 是本实验统一的时间布局。
// Go 用固定参考时间 2006-01-02 15:04:05 MST 定义格式，而非 printf 式占位符。
const layout = "2006-01-02 15:04:05"

// CustomTime 基于 time.Time 的自定义类型，接管 JSON 编解码行为。
type CustomTime time.Time

// MarshalJSON 实现 json.Marshaler 接口。
// 接口签名固定：func (t CustomTime) MarshalJSON() ([]byte, error)
// 必须返回带引号的合法 JSON 字符串。
func (t CustomTime) MarshalJSON() ([]byte, error) {
	stamp := fmt.Sprintf("\"%s\"", time.Time(t).Format(layout))
	return []byte(stamp), nil
}

// UnmarshalJSON 实现 json.Unmarshaler 接口。
// 接口签名固定：func (t *CustomTime) UnmarshalJSON(data []byte) error
// 注意接收者必须是指针，否则修改无法写回调用方。
func (t *CustomTime) UnmarshalJSON(data []byte) error {
	// data 是原始 JSON 字节（含引号），先去掉引号得到时间字符串
	s := strings.Trim(string(data), "\"")
	if s == "null" {
		t.setTime(time.Time{})
		return nil
	}
	if s == "0001-01-01 00:00:00" {
		t.setTime(time.Time{})
		return nil
	}
	parsed, err := time.ParseInLocation(layout, s, time.Local)
	if err != nil {
		return fmt.Errorf("CustomTime 只接受 %q 格式，收到 %q: %w", layout, s, err)
	}
	t.setTime(parsed)
	return nil
}

func (t *CustomTime) setTime(v time.Time) { *t = CustomTime(v) }

// Event 演示自定义类型嵌在 struct 里的效果。
type Event struct {
	Name string     `json:"name"`
	At   CustomTime `json:"at"`
}

func main() {
	// ---------- 1. struct -> 自定义格式字符串（MarshalJSON 被自动调用） ----------
	ev := Event{Name: "发布 v1.0", At: CustomTime(time.Date(2026, 9, 24, 10, 30, 0, 0, time.Local))}
	out, err := json.Marshal(ev)
	must(err, "marshal")
	fmt.Println("========== 1. MarshalJSON：struct -> 自定义格式 ==========")
	fmt.Printf("原始时间: %s\n", time.Time(ev.At).Format(layout))
	fmt.Printf("序列化结果: %s\n", out)

	// ---------- 2. 字符串 -> struct（UnmarshalJSON 被自动调用） ----------
	var back Event
	must(json.Unmarshal(out, &back), "unmarshal")
	fmt.Println("\n========== 2. UnmarshalJSON：自定义格式 -> struct ==========")
	fmt.Printf("还原结果: name=%s at=%s | round-trip 一致: %v\n",
		back.Name, time.Time(back.At).Format(layout), time.Time(back.At).Equal(time.Time(ev.At)))

	// ---------- 3. 坏格式：明确报错而不是静默零值 ----------
	bad := []byte(`{"name":"坏数据","at":"24/09/2026 10:30"}`)
	var badEv Event
	err = json.Unmarshal(bad, &badEv)
	fmt.Println("\n========== 3. 格式错误的处理 ==========")
	fmt.Printf("输入: %s\n", bad)
	fmt.Printf("解析错误: %v\n", err)
	fmt.Printf("错误对象未改动: name=%q at=%q\n", badEv.Name, time.Time(badEv.At).Format(layout))

	// ---------- 4. 零值与对照：默认 time.Time 的 RFC3339 格式 ----------
	var zero Event
	zeroJSON, _ := json.Marshal(zero)
	var zeroBack Event
	must(json.Unmarshal(zeroJSON, &zeroBack), "unmarshal zero")
	std, _ := json.Marshal(map[string]time.Time{"std": time.Time(ev.At)})
	fmt.Println("\n========== 4. 零值与默认格式对照 ==========")
	fmt.Printf("零值 Event 序列化: %s\n", zeroJSON)
	fmt.Printf("零值还原后再序列化: %s\n", mustMarshal(&zeroBack))
	fmt.Printf("默认 time.Time（未自定义）: %s\n", std)
}

func mustMarshal(v any) string {
	b, err := json.Marshal(v)
	must(err, "marshal")
	return string(b)
}

func must(err error, what string) {
	if err != nil {
		panic(fmt.Sprintf("%s: %v", what, err))
	}
}
