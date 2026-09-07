package main

import "testing"

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		p := new(Payload)
		sink = p
	}
}

func BenchmarkPrealloc(b *testing.B) {
	for i := 0; i < b.N; i++ {
		s := make([]int, 0, 256)
		for j := 0; j < 256; j++ {
			s = append(s, j)
		}
		sink = s
	}
}

func BenchmarkAppendGrow(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var s []int
		for j := 0; j < 256; j++ {
			s = append(s, j)
		}
		sink = s
	}
}
