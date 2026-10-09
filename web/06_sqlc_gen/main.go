// 实验 06：sqlc 代码生成 vs 手写 SQL。
//
// 同一个 PG、同一个接口，两条实现路线对照：
//
//	第 1 步：双路并跑——手写（database/sql）与生成码（pgx + gen/）各走一遍 CRUD
//	第 2 步：生成码速览——签名与参数结构体，类型安全在编译期定型
//	第 3 步：工作流对照——schema 变更时，手写要人肉找注入点，生成码重新 generate
//
// 前置：sqlc generate 已在仓库内执行过（gen/ 目录是产出物，可随时重生成）；
// PG 容器先起：../05_pg_migration/pg.sh start。断言见 go test -race。
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// 本篇用独立数据库 marklet06（幂等创建），与实验 05 的共享库隔离
const pgDBName = "marklet06"

var pgDSN = withDB(pgDBName)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// smoke：对任意实现跑同一串 CRUD，返回一行摘要（两路输出应一致）。
func smoke(ctx context.Context, s Bookmarks) string {
	id, err := s.Insert(ctx, "https://go.dev", "Go 官网")
	if err != nil {
		fatal(err)
	}
	b, err := s.Get(ctx, id)
	if err != nil {
		fatal(err)
	}
	if ok, err := s.Update(ctx, id, "Go 官网（改）"); err != nil || !ok {
		fatal(err)
	}
	got, _ := s.Get(ctx, id)
	deleted, err := s.Delete(ctx, id)
	if err != nil || !deleted {
		fatal(err)
	}
	return fmt.Sprintf("id=%d → %q → %q → 删除", id, b.Title, got.Title)
}

func main() {
	ctx := context.Background()
	if err := EnsurePGDatabase(pgDBName); err != nil {
		fatal(err)
	}

	manual, err := OpenManual(pgDSN)
	if err != nil {
		fatal(err)
	}
	defer manual.Close()
	generated, err := OpenSQLC(pgDSN)
	if err != nil {
		fatal(err)
	}
	defer generated.Close()
	if _, err := manual.Migrate(); err != nil {
		fatal(err)
	}

	fmt.Println("== 第 1 步：双路并跑——手写 SQL 与 sqlc 生成码，同一接口同一结果 ==")
	fmt.Printf("  手写   → %s\n", smoke(ctx, manual))
	fmt.Printf("  生成码 → %s\n", smoke(ctx, generated))
	fmt.Println("  ⇒ 结果一致；差异不在运行时，在 schema 变更那一刻由谁兜底。")

	fmt.Println("== 第 2 步：生成码速览——签名即文档，类型即约束 ==")
	showGen("gen/bookmarks.sql.go", "func (q *Queries) CreateBookmark")
	showGen("gen/bookmarks.sql.go", "type CreateBookmarkParams struct")
	showGen("gen/models.go", "type Bookmark struct")

	fmt.Println("== 第 3 步：schema 变更时——三层防线的对照 ==")
	fmt.Println("  手写漏改：加一列 NOT NULL 后 INSERT 照旧 → 运行时才被 PG 拒绝（测试 TestSchemaChangeWorkflow 实录）")
	fmt.Println("  生成码①：改列名后 SQL 与 schema 漂移 → sqlc generate 当场报错（README 有实录输出）")
	fmt.Println("  生成码②：列名/类型真变了 → 生成码同步改名，调用点直接 Go 编译报错")
}

// showGen：从生成文件里抽出声明展示——函数只取签名一行，结构体取完整块。
func showGen(path, prefix string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if strings.HasPrefix(prefix, "func") {
			fmt.Println("  " + line) // 签名即文档，函数体不必展示
			return
		}
		for end := i; end < len(lines); end++ {
			if strings.HasPrefix(lines[end], "}") {
				fmt.Println("  " + strings.Join(lines[i:end+1], "\n  "))
				return
			}
		}
	}
	fmt.Printf("  （%s 未找到 %s）\n", path, prefix)
}
