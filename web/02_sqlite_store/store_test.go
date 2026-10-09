package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// goleak 门禁：连接池未关干净（残留的数据库后台 goroutine）在这里现形。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// newTestStore：临时目录里的独立数据库，测试结束自动关闭并清理。
// 只开库不迁移——需要 schema 的测试自己调 Migrate（顺序本身就是要教的事）。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestMigrateIdempotent：验收①——迁移连跑两次，第二次必须零执行。
func TestMigrateIdempotent(t *testing.T) {
	s := newTestStore(t)

	first, err := s.Migrate()
	if err != nil {
		t.Fatalf("第一次迁移: %v", err)
	}
	if len(first) != len(migrations) {
		t.Errorf("第一次迁移执行了 %d 个版本， want %d", len(first), len(migrations))
	}
	for i, name := range first {
		if name != migrations[i].name {
			t.Errorf("第 %d 个版本名 = %q， want %q", i+1, name, migrations[i].name)
		}
	}

	second, err := s.Migrate()
	if err != nil {
		t.Fatalf("第二次迁移: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("第二次迁移执行了 %v， want 空（幂等被破坏）", second)
	}

	// 版本表里恰好记录了全部版本，一条不多
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(migrations) {
		t.Errorf("schema_migrations 有 %d 行， want %d", n, len(migrations))
	}
}

// TestMigrationSurvivesReopen：版本记录落了盘——关库重开后迁移仍是零执行，
// 且数据还在。这断言"迁移状态在数据库里"而不是在进程内存里。
func TestMigrationSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reopen.db")

	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	id, err := s.Insert(ctx, "https://go.dev", "Go 官网")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关库: %v", err)
	}

	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s2.Close() })

	again, err := s2.Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("重开后再迁移执行了 %v， want 空", again)
	}
	if _, err := s2.Get(ctx, id); err != nil {
		t.Errorf("重开后原数据丢失: %v", err)
	}
}

// TestCRUDRoundtrip：验收②的骨干——增查列删全链路。
// URL 带单引号：参数化查询下只是普通字符（注入防线的演示）。
func TestCRUDRoundtrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}

	id, err := s.Insert(ctx, "https://go.dev/blog/context?quote='x'", "context 包公式")
	if err != nil {
		t.Fatalf("插入: %v", err)
	}
	if id <= 0 {
		t.Fatalf("插入返回 id=%d， want 正数", id)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("查询: %v", err)
	}
	if got.URL != "https://go.dev/blog/context?quote='x'" || got.Title != "context 包公式" {
		t.Errorf("回读不一致: %+v", got)
	}
	if got.CreatedAt.IsZero() || time.Since(got.CreatedAt) > time.Minute {
		t.Errorf("created_at 不合理: %v", got.CreatedAt)
	}

	// 再插一条，列表应两条且新 id 在前
	if _, err := s.Insert(ctx, "https://pkg.go.dev", "标准库文档"); err != nil {
		t.Fatal(err)
	}
	all, err := s.List(ctx)
	if err != nil {
		t.Fatalf("列表: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("列表 %d 条， want 2", len(all))
	}
	if all[0].ID < all[1].ID {
		t.Errorf("列表未按 id 倒序: %d 在 %d 前", all[0].ID, all[1].ID)
	}

	deleted, err := s.Delete(ctx, id)
	if err != nil || !deleted {
		t.Fatalf("删除: deleted=%v err=%v", deleted, err)
	}
	if _, err := s.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("删除后 Get err = %v， want ErrNotFound", err)
	}
	if ok, err := s.Delete(ctx, id); err != nil || ok {
		t.Errorf("删除不存在的 id：deleted=%v err=%v， want false/nil", ok, err)
	}
}

// TestConcurrentReadWriteNoBusy：验收③——WAL + busy_timeout 下，
// 50 goroutine × 20 次混合读写 1000 次操作零错误（尤其零 SQLITE_BUSY）。
func TestConcurrentReadWriteNoBusy(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	const workers, perWorker = 50, 20
	errCh := make(chan error, workers*perWorker)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				var err error
				if i%5 == 0 {
					_, err = s.Insert(ctx, "https://example.com/concurrent", "并发写")
				} else {
					_, err = s.List(ctx)
				}
				if err != nil {
					errCh <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if len(errs) != 0 {
		t.Fatalf("1000 次混合读写出现 %d 个错误，首错: %v", len(errs), errs[0])
	}
}
