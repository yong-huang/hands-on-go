// 实验 05：Docker 化 PostgreSQL + 迁移移植。
//
// marklet 的存储层升级到生产形态：同一 Bookmarks 接口换上 PG 实现（pgx 驱动），
// 迁移 SQL 移植成 PG 方言。PG 容器由 ./pg.sh 管理（postgres:15-alpine，15432）。
//
//	第 1 步：双实现并跑——同一串 CRUD 在 SQLite 与 PG 上各走一遍，结果一致
//	第 2 步：方言差异——自增主键 AUTOINCREMENT vs BIGSERIAL，插入回 id
//	        一个靠 LastInsertId、一个靠 RETURNING
//	第 3 步：PG 迁移幂等——版本表在 PG 上照常工作，第二次零执行
//
// 运行方式：./pg.sh start && go run .（临时 SQLite 文件 + 共享 PG 库）；
// 断言见 go test -race（PG 用例在容器未启动时自动跳过，README 有诚实预期）。
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
)

const pgDSN = "postgres://marklet:marklet@127.0.0.1:15432/marklet?sslmode=disable"

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// smoke：对任意实现跑同一串 CRUD，返回一行摘要——两个实现跑出的摘要应一致。
func smoke(ctx context.Context, s Bookmarks) string {
	id, err := s.Insert(ctx, "https://go.dev", "Go 官网")
	if err != nil {
		fatal(err)
	}
	b, err := s.Get(ctx, id)
	if err != nil {
		fatal(err)
	}
	ok, err := s.Update(ctx, id, "Go 官网（改）")
	if err != nil || !ok {
		fatal(err)
	}
	deleted, err := s.Delete(ctx, id)
	if err != nil || !deleted {
		fatal(err)
	}
	if _, err := s.Get(ctx, id); err != ErrNotFound {
		fatal(fmt.Errorf("删除后 Get 应 ErrNotFound，得到 %v", err))
	}
	return fmt.Sprintf("INSERT id=%d → GET %q → UPDATE → DELETE，再查 ErrNotFound", id, b.Title)
}

func main() {
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "marklet05-*")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(dir)

	sqlite, err := OpenSQLite(dir + "/demo.db")
	if err != nil {
		fatal(err)
	}
	defer sqlite.Close()
	pg, err := OpenPG(pgDSN)
	if err != nil {
		fatal(err)
	}
	defer pg.Close()

	fmt.Println("== 第 1 步：双实现并跑——同一串 CRUD，同一套接口 ==")
	for _, s := range []Bookmarks{sqlite, pg} {
		if _, err := s.Migrate(); err != nil {
			fatal(err)
		}
		fmt.Printf("  %-8s → %s\n", dialectOf(s), smoke(ctx, s))
	}
	fmt.Println("  ⇒ 业务代码只认 Bookmarks 接口，换引擎零改动——这就是接口多态的实证。")

	fmt.Println("== 第 2 步：方言差异——同一张表，两种建法 ==")
	fmt.Printf("  sqlite 主键列: %s\n", pickLine(migrations[0].sqlite, "INTEGER PRIMARY KEY"))
	fmt.Printf("  pg     主键列: %s\n", pickLine(migrations[0].pgsql, "BIGSERIAL PRIMARY KEY"))
	fmt.Println("  ⇒ AUTOINCREMENT vs BIGSERIAL；回 id 一个走 LastInsertId，一个走 RETURNING。")

	fmt.Println("== 第 3 步：PG 迁移幂等——清库重演一遍完整迁移 ==")
	// 演示库前面步骤已经迁移过，这里清掉重建，让"首次全量应用"真实发生一次
	pgStore := pg.(*sqlStore)
	if _, err := pgStore.db.Exec(`DROP TABLE IF EXISTS bookmarks, schema_migrations CASCADE`); err != nil {
		fatal(err)
	}
	applied, err := pg.Migrate()
	if err != nil {
		fatal(err)
	}
	fmt.Printf("  清库后首次：应用 %d 个版本 %v\n", len(applied), applied)
	again, err := pg.Migrate()
	if err != nil {
		fatal(err)
	}
	fmt.Printf("  紧接着重跑：应用 %d 个版本——幂等在 PG 上同样成立\n", len(again))
}

// dialectOf：演示里区分实现用的名字。
func dialectOf(s Bookmarks) string {
	if v, ok := s.(*sqlStore); ok {
		return v.Kind()
	}
	return "unknown"
}

// pickLine：从多行 DDL 里取包含指定片段的那一行（方言差异看主键列最直观）。
func pickLine(s, substr string) string {
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			line := s[start:i]
			if strings.Contains(line, substr) {
				return strings.TrimSpace(line)
			}
			start = i + 1
		}
	}
	return "(未找到 " + substr + ")"
}
