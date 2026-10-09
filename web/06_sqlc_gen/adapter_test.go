package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// goleak 门禁。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var testDSN = withDB("marklet06") // 独立数据库，防跨包并行互踩

var ensureDBErr error

func init() {
	// 建库失败不 panic——init 炸掉整个测试二进制会让"PG 不可用时优雅 Skip"
	// 的降级失效（完整测试实测教训）。错误记录于此，由各用例自行 Skip。
	ensureDBErr = EnsurePGDatabase("marklet06")
}

// mustOpen：PG 不可用就跳过（诚实标注）。
func mustOpen[T Bookmarks](t *testing.T, open func(string) (T, error)) T {
	t.Helper()
	if ensureDBErr != nil { // init 建库失败：在此降级为 Skip，而非 panic
		t.Skipf("PG 不可用（../05_pg_migration/pg.sh start 可启动）：%v", ensureDBErr)
	}
	s, err := open(testDSN)
	if err != nil {
		t.Skipf("PG 不可用（../05_pg_migration/pg.sh start 可启动）：%v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestGeneratedMatchesManual：验收①——生成码通过与手写版完全相同的集成用例，
// 且两边对同一操作返回逐字段一致的结果。
func TestGeneratedMatchesManual(t *testing.T) {
	ctx := context.Background()
	manual := mustOpen(t, OpenManual)
	generated := mustOpen(t, OpenSQLC)
	if _, err := manual.Migrate(); err != nil {
		t.Fatal(err)
	}

	exercise(t, "manual", manual)
	exercise(t, "sqlc", generated)

	// 同一操作，两路结果逐字段一致
	mid, err := manual.Insert(ctx, "https://example.com/agree", "对照")
	if err != nil {
		t.Fatal(err)
	}
	gid, err := generated.Insert(ctx, "https://example.com/agree", "对照")
	if err != nil {
		t.Fatal(err)
	}
	mb, err := manual.Get(ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	gb, err := generated.Get(ctx, gid)
	if err != nil {
		t.Fatal(err)
	}
	if mb.URL != gb.URL || mb.Title != gb.Title ||
		mb.CreatedAt.Sub(gb.CreatedAt).Abs() > 2*time.Second {
		t.Errorf("两路结果不一致:\n manual: %+v\n sqlc:   %+v", mb, gb)
	}
}

// exercise：单实现的集成用例（CRUD 全链路 + 错误语义）。
func exercise(t *testing.T, name string, s Bookmarks) {
	t.Helper()
	ctx := context.Background()

	id, err := s.Insert(ctx, "https://go.dev/x?quote='y'", "带引号")
	if err != nil {
		t.Fatalf("[%s] 插入: %v", name, err)
	}
	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("[%s] 查询: %v", name, err)
	}
	if got.URL != "https://go.dev/x?quote='y'" || got.Title != "带引号" {
		t.Errorf("[%s] 回读不一致: %+v", name, got)
	}
	if got.CreatedAt.IsZero() || time.Since(got.CreatedAt) > time.Minute {
		t.Errorf("[%s] created_at 不合理: %v", name, got.CreatedAt)
	}

	if ok, err := s.Update(ctx, id, "改"); err != nil || !ok {
		t.Fatalf("[%s] 更新: ok=%v err=%v", name, ok, err)
	}
	if g, _ := s.Get(ctx, id); g.Title != "改" {
		t.Errorf("[%s] 更新未生效: %q", name, g.Title)
	}
	if ok, err := s.Delete(ctx, id); err != nil || !ok {
		t.Fatalf("[%s] 删除: ok=%v err=%v", name, ok, err)
	}
	if _, err := s.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("[%s] 删除后 Get err = %v， want ErrNotFound", name, err)
	}
	if ok, err := s.Update(ctx, id, "改不存在"); err != nil || ok {
		t.Errorf("[%s] 改不存在：ok=%v err=%v， want false", name, ok, err)
	}
}

// TestSchemaChangeWorkflow：验收②的可执行化——模拟"schema 加了 NOT NULL 列"后，
// 手写 INSERT 漏改的后果是运行时错误（PG 拒绝写入），而 sqlc 的对应物是编译错误
// （ regeneration 后缺字段直接 build 不过，本测试断言生成码当前 schema 下编译通过，
// 编译失败对照在 README 有实录）。
func TestSchemaChangeWorkflow(t *testing.T) {
	s := mustOpen(t, OpenManual)
	ctx := context.Background()
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}

	// 现场：加一列 NOT NULL 无默认值的列——等价于 schema 变更落地到真实库
	if _, err := s.(*manualStore).db.ExecContext(ctx,
		`ALTER TABLE bookmarks ADD COLUMN note TEXT`); err != nil {
		t.Fatalf("加列: %v", err)
	}
	t.Cleanup(func() {
		s.(*manualStore).db.ExecContext(context.Background(), `ALTER TABLE bookmarks DROP COLUMN note`)
	})

	// 给 note 加 NOT NULL 约束，模拟"漏改的手写 INSERT 即将翻车"的前置
	// （先回填存量行，否则约束加不上——这本身就是一次真实的迁移操作）
	if _, err := s.(*manualStore).db.ExecContext(ctx,
		`UPDATE bookmarks SET note = '' WHERE note IS NULL`); err != nil {
		t.Fatalf("回填: %v", err)
	}
	if _, err := s.(*manualStore).db.ExecContext(ctx,
		`ALTER TABLE bookmarks ALTER COLUMN note SET NOT NULL`); err != nil {
		t.Fatalf("加约束: %v", err)
	}

	// 手写 INSERT 没带 note → 运行时才炸（这正是要被 sqlc 编译期拦截的错误类别）
	_, err := s.Insert(ctx, "https://example.com/miss", "漏列")
	if err == nil {
		t.Fatal("手写 INSERT 漏 NOT NULL 列应当运行时报错，却成功了")
	}
	t.Logf("手写漏列的运行时错误（对照 sqlc 的编译期报错）: %v", err)

	// 清掉约束后恢复可用，证明库没坏
	if _, err := s.(*manualStore).db.ExecContext(ctx,
		`ALTER TABLE bookmarks ALTER COLUMN note DROP NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Insert(ctx, "https://example.com/ok", "恢复"); err != nil {
		t.Fatalf("恢复后插入失败: %v", err)
	}
}
