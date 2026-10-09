package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// goleak 门禁：连接池没关干净在这里现形。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const testPGDSN = "postgres://marklet:marklet@127.0.0.1:15432/marklet?sslmode=disable"

// openPGOrSkip：PG 容器没起就跳过（诚实标注，而不是假装没这个用例）。
func openPGOrSkip(t *testing.T) Bookmarks {
	t.Helper()
	s, err := OpenPG(testPGDSN)
	if err != nil {
		t.Skipf("PG 不可用（./pg.sh start 可启动）：%v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestSharedSuiteOnSQLiteAndPG：验收②——同一套集成用例，SQLite 与 PG 都绿。
// 用例函数只认 Bookmarks 接口，这就是接口多态在测试侧的兑现。
// SQLite 用一次性临时库（fresh=true 可断言首次全量迁移）；
// PG 是跨用例共享的容器库，fresh 语义由 TestPGMigrateFreshAndIdempotent 单独覆盖。
func TestSharedSuiteOnSQLiteAndPG(t *testing.T) {
	sqlite, err := OpenSQLite(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlite.Close() })
	exerciseStore(t, "sqlite", sqlite)

	exerciseStore(t, "postgres", openPGOrSkip(t))
}

// exerciseStore：跨方言的集成用例——CRUD 全链路 + 时间语义 + 迁移幂等（重跑零执行）。
func exerciseStore(t *testing.T, name string, s Bookmarks) {
	t.Helper()
	ctx := context.Background()

	if _, err := s.Migrate(); err != nil {
		t.Fatalf("[%s] 迁移: %v", name, err)
	}
	// 幂等：无论库是全新还是已迁移过，重跑一次必须零执行
	if again, err := s.Migrate(); err != nil || len(again) != 0 {
		t.Fatalf("[%s] 重跑迁移 applied=%v err=%v， want 空", name, again, err)
	}

	// CRUD 全链路
	id, err := s.Insert(ctx, "https://go.dev/blog/x?quote='y'", "带引号标题")
	if err != nil {
		t.Fatalf("[%s] 插入: %v", name, err)
	}
	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("[%s] 查询: %v", name, err)
	}
	if got.URL != "https://go.dev/blog/x?quote='y'" || got.Title != "带引号标题" {
		t.Errorf("[%s] 回读不一致: %+v", name, got)
	}
	if got.CreatedAt.IsZero() || time.Since(got.CreatedAt) > time.Minute {
		t.Errorf("[%s] created_at 不合理: %v", name, got.CreatedAt)
	}

	if ok, err := s.Update(ctx, id, "改后标题"); err != nil || !ok {
		t.Fatalf("[%s] 更新: ok=%v err=%v", name, ok, err)
	}
	if g, _ := s.Get(ctx, id); g.Title != "改后标题" {
		t.Errorf("[%s] 更新未生效: %q", name, g.Title)
	}

	if ok, err := s.Delete(ctx, id); err != nil || !ok {
		t.Fatalf("[%s] 删除: ok=%v err=%v", name, ok, err)
	}
	if _, err := s.Get(ctx, id); err != ErrNotFound {
		t.Errorf("[%s] 删除后 Get err = %v， want ErrNotFound", name, err)
	}
	if ok, err := s.Delete(ctx, id); err != nil || ok {
		t.Errorf("[%s] 删不存在的 id：ok=%v err=%v， want false", name, ok, err)
	}
}

// TestPGMigrateFreshAndIdempotent：验收③——PG 上从零（清库）迁移全量应用，
// 重跑零执行。共享容器库的"首次"语义靠清库制造，共享与幂等两不误。
func TestPGMigrateFreshAndIdempotent(t *testing.T) {
	s := openPGOrSkip(t)
	st := s.(*sqlStore) // 同包测试，直接取底层连接池做清库
	if _, err := st.db.Exec(`DROP TABLE IF EXISTS bookmarks, schema_migrations CASCADE`); err != nil {
		t.Fatalf("清库: %v", err)
	}

	applied, err := s.Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != len(migrations) {
		t.Fatalf("清库后迁移应用 %d 个版本， want %d", len(applied), len(migrations))
	}
	if again, err := s.Migrate(); err != nil || len(again) != 0 {
		t.Fatalf("重跑迁移 applied=%v err=%v， want 空", again, err)
	}
}

// TestPGSuiteRepeatable：同一套用例在共享 PG 库上连跑两轮——幂等保证可重复。
func TestPGSuiteRepeatable(t *testing.T) {
	s := openPGOrSkip(t)
	for i := 0; i < 2; i++ {
		t.Run(fmt.Sprintf("round-%d", i+1), func(t *testing.T) {
			exerciseStore(t, fmt.Sprintf("pg-round-%d", i+1), s)
		})
	}
}

// TestDialectSQLReallyDiffers：方言差异是"故意的"，钉住它——
// 防止后人把方言列删了让两个实现悄悄趋同。
func TestDialectSQLReallyDiffers(t *testing.T) {
	m := migrations[0]
	if m.sqlite == m.pgsql {
		t.Fatal("001 的两个方言 SQL 相同——方言差异被意外抹平")
	}
	if !strings.Contains(m.sqlite, "AUTOINCREMENT") || !strings.Contains(m.pgsql, "BIGSERIAL") {
		t.Fatal("自增主键的方言写法丢失")
	}
}
