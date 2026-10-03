//go:build !race

// raceEnabled：普通构建（无 -race）时为 false。
package main

const raceEnabled = false
