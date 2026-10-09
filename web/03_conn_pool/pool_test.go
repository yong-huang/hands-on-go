package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// goleak 门禁：哪个测试没关干净连接池，后台连接 goroutine 会在这里现形。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// newBenchDB：临时目录里的干净库（建表 + 预置数据），测试结束自动关闭。
func newBenchDB(t *testing.T, cfg PoolConfig) *sql.DB {
	t.Helper()
	db, err := openPool(filepath.Join(t.TempDir(), "bench.db"), cfg)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := mustSchema(db); err != nil {
		t.Fatalf("建表: %v", err)
	}
	if err := seed(db, 50); err != nil {
		t.Fatalf("预置: %v", err)
	}
	return db
}

// TestCappedPoolMakesRequestsWait：验收①的核心断言——
// MaxOpen=2 而 20 并发，多数请求必然在池队列排队（WaitCount 显著大于零），
// 同时在途连接从不超过 2（池上限是硬约束，不是建议）。
func TestCappedPoolMakesRequestsWait(t *testing.T) {
	db := newBenchDB(t, PoolConfig{MaxOpen: 2, MaxIdle: 2})

	st, err := runRead(db, 20, 5) // 100 个请求，并发 20
	if err != nil {
		t.Fatal(err)
	}
	if st.errs != 0 {
		t.Fatalf("读负载不应出错，得到 %d 个", st.errs)
	}
	if st.waitCount < 50 {
		t.Errorf("WaitCount=%d，20 并发打 2 上限应有一半以上请求排队", st.waitCount)
	}
	if st.p99 <= st.p50 {
		t.Errorf("排队未拉高尾巴：p99=%v 应明显大于 p50=%v", st.p99, st.p50)
	}
}

// TestUnlimitedPoolNeverWaits：对照组——MaxOpen=0（不限）时，
// 每个请求拿连接即用，WaitCount 必须恰好为 0。
func TestUnlimitedPoolNeverWaits(t *testing.T) {
	db := newBenchDB(t, PoolConfig{MaxOpen: 0, MaxIdle: 2})

	st, err := runRead(db, 20, 5)
	if err != nil {
		t.Fatal(err)
	}
	if st.waitCount != 0 {
		t.Errorf("不设上限时 WaitCount=%d， want 0（排队不该发生）", st.waitCount)
	}
}

// TestMaxIdleZeroClosesIdle：MaxIdle=0 的代价——每条连接用完即关，
// 结束后池里不留一条空闲连接，重开成本全摊在每个请求上。
func TestMaxIdleZeroClosesIdle(t *testing.T) {
	db := newBenchDB(t, PoolConfig{MaxOpen: 4, MaxIdle: 0})

	st, err := runRead(db, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if st.errs != 0 {
		t.Fatalf("读负载不应出错，得到 %d 个", st.errs)
	}
	time.Sleep(50 * time.Millisecond) // 给异步关闭留一拍
	if got := db.Stats().OpenConnections; got != 0 {
		t.Errorf("Idle=0 时收尾应无空闲连接，实际 %d 条", got)
	}
}

// TestWriteBurstNoBusy：写负载叠上单写者约束——busy_timeout(5000) 兜底，
// 20 并发 × 10 写共 200 次写入零错误、零丢失。
func TestWriteBurstNoBusy(t *testing.T) {
	db := newBenchDB(t, PoolConfig{MaxOpen: 8, MaxIdle: 8})

	st, err := runWrite(db, 20, 10)
	if err != nil {
		t.Fatal(err)
	}
	if st.errs != 0 {
		t.Fatalf("busy_timeout 应兜住写锁竞争，实际 %d 个错误", st.errs)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bookmarks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 50+200 { // 种子 50 行 + 写入 200 行
		t.Errorf("落库 %d 行， want 250（写入不能丢）", n)
	}
}

// TestLifetimeRecycles：SetConnMaxLifetime 到期的连接会被回收
// （Stats.MaxLifetimeClosed 增长）——用小步重试轮到它发生，避免计时脆弱。
func TestLifetimeRecycles(t *testing.T) {
	db := newBenchDB(t, PoolConfig{MaxOpen: 2, MaxIdle: 2, Lifetime: 20 * time.Millisecond})

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := runRead(db, 5, 10); err != nil {
			t.Fatal(err)
		}
		if db.Stats().MaxLifetimeClosed > 0 {
			return // 到期回收发生了
		}
		if time.Now().After(deadline) {
			t.Fatal("3s 内没有连接因 Lifetime 到期被回收")
		}
	}
}
