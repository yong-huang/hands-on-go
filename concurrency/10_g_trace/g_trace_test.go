package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime/trace"
	"testing"
	"time"
)

// TestTraceFileGenerated：录制与主程序相同的混合负载到临时文件，
// 断言 trace 落盘且是合法的 trace 文件（魔数以 "go 1." 开头）。
// 状态时序本身是调度器决定的，不做确定性断言。
func TestTraceFileGenerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workload.out")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := trace.Start(f); err != nil {
		t.Fatalf("trace.Start: %v", err)
	}
	runWorkload()
	trace.Stop()
	f.Close()

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size() < 16*1024 {
		t.Fatalf("trace 文件只有 %d 字节，混合负载的事件量异常", st.Size())
	}
	head, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.HasPrefix(head, []byte("go 1.")) {
		t.Fatalf("trace 魔数异常: %q", head[:16])
	}
}

// TestWorkloadCompletes：不开 trace 时负载也能正常完成（结构断言级别）。
func TestWorkloadCompletes(t *testing.T) {
	done := make(chan struct{})
	go func() {
		runWorkload()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("runWorkload 30s 未完成，疑似阻塞")
	}
	if got := sink.Load(); got == 0 {
		t.Fatal("sink=0：自旋循环疑似被优化掉")
	}
	t.Logf("sink=%d", sink.Load())
}
