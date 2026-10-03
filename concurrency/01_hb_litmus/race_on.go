//go:build race

// raceEnabled：当前构建带 -race 时为 true（标准库测试常用的 build tag 手法）。
package main

const raceEnabled = true
