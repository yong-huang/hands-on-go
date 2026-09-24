package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 用例 1：正常 round-trip —— marshal 出自定义格式，unmarshal 还原一致。
func TestRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 24, 10, 30, 0, 0, time.Local)
	ev := Event{Name: "发布 v1.0", At: CustomTime(at)}

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); !strings.Contains(got, `"2026-09-24 10:30:00"`) {
		t.Fatalf("期望包含自定义格式时间，得到 %s", got)
	}

	var back Event
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Name != ev.Name || !time.Time(back.At).Equal(at) {
		t.Fatalf("round-trip 不一致: %+v vs %+v", back, ev)
	}
}

// 用例 2：坏格式 —— UnmarshalJSON 必须返回明确错误，且不静默清零。
func TestBadFormat(t *testing.T) {
	var ev Event
	err := json.Unmarshal([]byte(`{"name":"x","at":"24/09/2026 10:30"}`), &ev)
	if err == nil {
		t.Fatal("坏格式应返回错误，得到 nil")
	}
	if !strings.Contains(err.Error(), `只接受`) {
		t.Fatalf("错误信息应包含可读提示，得到 %v", err)
	}
	if !time.Time(ev.At).IsZero() {
		t.Fatalf("失败解析不应改动 At 字段: %+v", ev)
	}
	// 注：encoding/json 遇到字段错误会继续解析其余字段，Name 已被赋值为 "x"
}

// 用例 3：零值 —— 零值序列化/反序列化稳定，等价于 0001-01-01 00:00:00。
func TestZeroValue(t *testing.T) {
	b, err := json.Marshal(Event{})
	if err != nil {
		t.Fatalf("marshal zero: %v", err)
	}
	if !strings.Contains(string(b), `"0001-01-01 00:00:00"`) {
		t.Fatalf("零值应序列化为 0001-01-01 00:00:00，得到 %s", b)
	}

	var back Event
	if err := json.Unmarshal([]byte(`{"name":"","at":"0001-01-01 00:00:00"}`), &back); err != nil {
		t.Fatalf("unmarshal zero: %v", err)
	}
	if !time.Time(back.At).IsZero() {
		t.Fatalf("应还原为 time.Time 零值: %v", time.Time(back.At))
	}
}
