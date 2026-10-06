package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

// BenchmarkParseOnly：纯解码开销（无网络）——64B 值的一对请求+响应流。
// 把"网络 RTT"与"解析成本"两个变量拆开：上面 ops/s 差距 <2%，这里看真实解析差距。
func BenchmarkParseOnly(b *testing.B) {
	value := strings.Repeat("v", 64)

	// 文本：SET 请求 + $64 响应 ≈ 92 字节
	textStream := []byte("SET k0001 " + value + "\r\n+OK\r\n$64\r\n" + value + "\r\n")
	// 二进制：SET 帧 + GET 帧头 + GET 响应帧 ≈ 89 字节
	binStream := []byte{}
	binStream = append(binStream, frameEncode(KindSet, "k0001 "+value)...)
	binStream = append(binStream, frameEncode(KindGet, "k0001")...)
	binStream = append(binStream, frameEncode(KindSet, value)...)

	b.Run("text", func(b *testing.B) {
		br := bufio.NewReader(bytes.NewReader(textStream))
		for i := 0; i < b.N; i++ {
			br.Reset(bytes.NewReader(textStream))
			readLine(br)
			readLine(br)
			readLine(br)
		}
	})
	b.Run("binary", func(b *testing.B) {
		br := bufio.NewReader(bytes.NewReader(binStream))
		for i := 0; i < b.N; i++ {
			br.Reset(bytes.NewReader(binStream))
			readFrame(br)
			readFrame(br)
			readFrame(br)
		}
	})
}
