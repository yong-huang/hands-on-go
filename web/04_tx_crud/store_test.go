package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// goleak 门禁（整个包共用一个 TestMain）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestMigrateAppliesNewVersions：迁移列表长到 3 个版本——新库全量应用，
// 重跑零执行（幂等不随版本增长而破坏）。
func TestMigrateAppliesNewVersions(t *testing.T) {
	s := newTestStore(t)

	first, err := s.Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(migrations) {
		t.Fatalf("首次迁移应用 %d 个版本， want %d", len(first), len(migrations))
	}
	if first[len(first)-1] != "003_add_counters" {
		t.Errorf("末版本 = %q， want 003_add_counters", first[len(first)-1])
	}
	second, err := s.Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Errorf("重跑应用 %v， want 空", second)
	}
}

// TestWithTxRollsBackOnError：fn 报错 → 事务回滚，库里不留半截写入。
func TestWithTxRollsBackOnError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.seedBalance(ctx, 100); err != nil {
		t.Fatal(err)
	}

	err := WithTx(ctx, s.DB(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE counters SET n = 0 WHERE id = 1`); err != nil {
			return err
		}
		return errors.New("boom") // 业务校验失败
	})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("WithTx err = %v， want boom", err)
	}
	if n, _ := s.readBalance(ctx); n != 100 {
		t.Errorf("回滚后余额 = %d， want 100（半截写入被回滚）", n)
	}
}

// TestWithTxRecoversPanic：fn panic → defer Rollback 兜底，不留悬挂事务。
func TestWithTxRecoversPanic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.seedBalance(ctx, 100); err != nil {
		t.Fatal(err)
	}

	func() {
		defer func() { recover() }() // 故意让 panic 冒出来再接住
		_ = WithTx(ctx, s.DB(), func(tx *sql.Tx) error {
			tx.Exec(`UPDATE counters SET n = 0 WHERE id = 1`)
			panic("业务炸了")
		})
	}()
	if n, _ := s.readBalance(ctx); n != 100 {
		t.Errorf("panic 后余额 = %d， want 100（defer Rollback 必须兜底）", n)
	}
}

// TestLostUpdateReproduced：验收①前半——无事务读-改-写必然丢更新。
// 用小步重试把"调度恰好没交错"的偶然性排除：丢更新一旦被观测到即 PASS。
func TestLostUpdateReproduced(t *testing.T) {
	for attempt := 1; attempt <= 5; attempt++ {
		s := newTestStore(t)
		ctx := context.Background()
		if _, err := s.Migrate(); err != nil {
			t.Fatal(err)
		}
		if _, err := s.seedBalance(ctx, 100); err != nil {
			t.Fatal(err)
		}

		runParallelTest(t, 10, 10, func() error { return s.decrementRacy(ctx, time.Millisecond) })
		n, err := s.readBalance(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return // 观测到丢更新：最终余额 > 0（有减法凭空消失）
		}
		t.Logf("第 %d 次尝试未观测到丢更新，重试", attempt)
	}
	t.Fatal("5 次尝试都没能复现丢更新（业务窗口可能没生效）")
}

// TestTxVersionConserves：验收①后半——事务版同样负载余额精确归零。
// BEGIN IMMEDIATE 串行化读-改-写，这是守恒的全部原因。
func TestTxVersionConserves(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.seedBalance(ctx, 100); err != nil {
		t.Fatal(err)
	}

	runParallelTest(t, 10, 10, func() error { return s.decrementTx(ctx, time.Millisecond) })
	n, err := s.readBalance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("事务版余额 = %d， want 0（守恒被打破）", n)
	}
}

// TestAtomicVersionConserves：原子 UPDATE 版同样精确归零——
// 自减进一条语句，业务窗口根本不存在。
func TestAtomicVersionConserves(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.seedBalance(ctx, 100); err != nil {
		t.Fatal(err)
	}

	runParallelTest(t, 10, 10, func() error { return s.decrementAtomic(ctx) })
	if n, _ := s.readBalance(ctx); n != 0 {
		t.Errorf("原子版余额 = %d， want 0", n)
	}
}

// runParallelTest：测试里的并发驱动器（与演示的 runParallel 同构）。
func runParallelTest(t *testing.T, workers, perWorker int, op func() error) {
	t.Helper()
	var wg sync.WaitGroup
	errCh := make(chan error, workers*perWorker)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if err := op(); err != nil {
					errCh <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("并发操作失败: %v", err)
	}
}
