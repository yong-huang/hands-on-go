//go:build race_demo

// race_demo 标签专用于生成 README 注解用的 `go test -race` 报告样例：
//
//	go test -race -tags race_demo -run TestDetectedRaceSample ./04_race_detector/
//
// 该测试故意包含真竞态，必然 FAIL——这是样例的来源，因此不进入常规
// `go test ./...`（无此标签时本文件不参与编译）。
package main

import "testing"

// TestDetectedRaceSample：运行 detectedRace()，让 -race 在 go test 环境
// 下输出带测试名、goroutine 创建点的完整报告（README 逐行注解的对象）。
func TestDetectedRaceSample(t *testing.T) {
	detectedRace()
}
