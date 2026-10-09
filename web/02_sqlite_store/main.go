// 实验 02：SQLite 持久层与手写迁移。
//
// marklet 的数据层落盘：纯 Go 驱动 modernc.org/sqlite（无 CGO）、
// schema_migrations 版本表迁移器（幂等）、参数化 CRUD、WAL 并发。
// 存储代码在 store.go，本文件只负责把它跑给你看：
//
//	第 1 步：开库——journal_mode 从默认 delete 变成 wal（写不阻塞读的前提）
//	第 2 步：迁移连跑两次——第一次执行 2 个版本，第二次零执行（幂等的定义）
//	第 3 步：CRUD 一轮——插入/查询/列表/删除，含带单引号的 URL（参数化防注入）
//	第 4 步：WAL 并发——50 goroutine × 20 次混合读写，BUSY 计数必须为 0
//
// 运行方式：go run .（数据库文件落在临时目录，退出即清理）；断言见 go test -race。
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func main() {
	dir, err := os.MkdirTemp("", "marklet02-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "创建临时目录失败:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	ctx := context.Background()

	store, err := OpenStore(filepath.Join(dir, "marklet.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "开库失败:", err)
		os.Exit(1)
	}
	defer store.Close()

	fmt.Println("== 第 1 步：开库——journal_mode 变为 wal ==")
	var mode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		fmt.Fprintln(os.Stderr, "读 journal_mode 失败:", err)
		os.Exit(1)
	}
	// SQLite 默认 journal_mode 是 delete（回滚日志，写独占）；
	// DSN 里的 _pragma=journal_mode(WAL) 把它切到预写日志。
	fmt.Printf("  PRAGMA journal_mode → %s（默认是 delete，WAL 才能写不阻塞读）\n", mode)

	fmt.Println("== 第 2 步：迁移连跑两次——第二次零执行 ==")
	applied, err := store.Migrate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "迁移失败:", err)
		os.Exit(1)
	}
	fmt.Printf("  第一次迁移：执行 %d 个版本 %v\n", len(applied), applied)
	again, err := store.Migrate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "二次迁移失败:", err)
		os.Exit(1)
	}
	fmt.Printf("  第二次迁移：执行 %d 个版本（已应用的直接跳过——这就是幂等）\n", len(again))

	fmt.Println("== 第 3 步：CRUD 一轮 ==")
	// URL 故意带单引号：参数化查询下它就是普通字符，注入无从谈起
	id, err := store.Insert(ctx, "https://go.dev/blog/context?it's= quoted", "context 包公式")
	if err != nil {
		fmt.Fprintln(os.Stderr, "插入失败:", err)
		os.Exit(1)
	}
	got, err := store.Get(ctx, id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "查询失败:", err)
		os.Exit(1)
	}
	fmt.Printf("  INSERT → id=%d，GET 回读：%s（%s）\n", id, got.Title, got.URL)
	all, err := store.List(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "列表失败:", err)
		os.Exit(1)
	}
	fmt.Printf("  LIST → %d 条\n", len(all))
	deleted, err := store.Delete(ctx, id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "删除失败:", err)
		os.Exit(1)
	}
	_, err = store.Get(ctx, id)
	fmt.Printf("  DELETE → 已删除=%v，再 GET → err=%v\n", deleted, err)

	fmt.Println("== 第 4 步：WAL 并发——50 goroutine × 20 次混合读写 ==")
	start := time.Now()
	busy, failed := runConcurrent(store)
	fmt.Printf("  完成 1000 次操作：错误 %d 个，其中 SQLITE_BUSY %d 个，耗时 %dms\n",
		failed, busy, time.Since(start).Milliseconds())
	fmt.Println("  ⇒ busy_timeout 让写锁竞争变成「排队等」而不是「立刻炸」，WAL 让读写互不阻塞。")
}

// runConcurrent：50 个 goroutine 各做 20 次混合操作（每 5 次一次写，其余读），
// 返回 {SQLITE_BUSY 错误数, 总错误数}。写锁竞争交给 busy_timeout 排队，
// 这正是验收③"无 SQLITE_BUSY"的演示口径。
func runConcurrent(s *Store) (busy, failed int) {
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
					_, err = s.Insert(ctx,
						fmt.Sprintf("https://example.com/%d/%d", w, i), "并发写")
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

	for err := range errCh {
		failed++
		// modernc 驱动对 SQLITE_BUSY 的报错文案含 "SQLITE_BUSY" 或 "locked"
		if strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "locked") {
			busy++
		}
	}
	return busy, failed
}
