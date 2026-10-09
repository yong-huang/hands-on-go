// 实验 03：连接池调参与并发实测。
//
// 池的三个旋钮（MaxOpen/MaxIdle/MaxLifetime）拧到不同档位，同一负载量差距：
//
//	第 1 步：读负载 × 5 组池配置对照——p50/p99/吞吐/等待次数四列看排队成本
//	第 2 步：WaitCount 对账——排队是否真实发生，池的账本和请求量对不对得上
//	第 3 步：写负载对照——池开得再大，SQLite 单写者才是硬瓶颈
//
// 读负载用预编译 SELECT（无写锁，池是唯一排队点，才能单独量出池的影响）；
// 写负载故意叠上 SQLite 单写者约束，看"池调优"在真瓶颈面前的边界。
//
// 运行方式：go run .（临时目录建库，退出即清理）；断言见 go test -race。
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// freshDB：一套干净的实验环境——独立库文件（等待计数互不串账）、建表、预置 100 行。
func freshDB(path string, cfg PoolConfig) *sql.DB {
	db, err := openPool(path, cfg)
	if err != nil {
		fatal(err)
	}
	if err := mustSchema(db); err != nil {
		fatal(err)
	}
	if err := seed(db, 100); err != nil {
		fatal(err)
	}
	return db
}

func main() {
	dir, err := os.MkdirTemp("", "marklet03-*")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(dir)

	fmt.Println("== 第 1 步：读负载 × 5 组池配置——50 goroutine × 40 读 = 2000 请求 ==")
	fmt.Println("   （每行独立库文件；waiters = 在池队列里排过队的请求数）")
	type bench struct {
		name string
		cfg  PoolConfig
	}
	for i, b := range []bench{
		{"Open=1（串行）", PoolConfig{MaxOpen: 1, MaxIdle: 1}},
		{"Open=2", PoolConfig{MaxOpen: 2, MaxIdle: 2}},
		{"Open=4 Idle=4（调优）", PoolConfig{MaxOpen: 4, MaxIdle: 4}},
		{"Open=不限（默认）", PoolConfig{MaxOpen: 0, MaxIdle: 2}},
		{"Open=4 Idle=0（不留闲）", PoolConfig{MaxOpen: 4, MaxIdle: 0}},
	} {
		db := freshDB(filepath.Join(dir, fmt.Sprintf("bench-%d.db", i)), b.cfg)
		st, err := runRead(db, 50, 40)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("  %-18s p50=%-9v p99=%-11v 吞吐=%5.0f/s  waiters=%4d  总等待=%v\n",
			b.name, st.p50.Round(time.Microsecond), st.p99.Round(time.Microsecond),
			st.throughpt, st.waitCount, st.waitDur.Round(time.Millisecond))
		db.Close()
	}

	fmt.Println("== 第 2 步：WaitCount 对账——排队有没有发生，池的账本说了算 ==")
	// Open=2 而 50 并发：多数请求必须排队，账本应记下接近 2000 的等待
	db2 := freshDB(filepath.Join(dir, "reconcile2.db"), PoolConfig{MaxOpen: 2, MaxIdle: 2})
	st, err := runRead(db2, 50, 40)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("  Open=2 ：2000 请求，WaitCount=%d（几乎人人排队），累计等待 %v\n",
		st.waitCount, st.waitDur.Round(time.Millisecond))
	db2.Close()
	// Open 不限：每请求拿连接即用，账本应一格不记
	dbU := freshDB(filepath.Join(dir, "reconcileU.db"), PoolConfig{MaxOpen: 0, MaxIdle: 2})
	st, err = runRead(dbU, 50, 40)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("  Open=不限：2000 请求，WaitCount=%d（池永不让人等）\n", st.waitCount)
	dbU.Close()

	fmt.Println("== 第 3 步：写负载对照——池不是瓶颈时，单写者才是 ==")
	for _, b := range []bench{
		{"写 × Open=2", PoolConfig{MaxOpen: 2, MaxIdle: 2}},
		{"写 × Open=8", PoolConfig{MaxOpen: 8, MaxIdle: 8}},
	} {
		db := freshDB(filepath.Join(dir, "write.db"), b.cfg)
		st, err := runWrite(db, 50, 10)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("  %-14s 500 次写入：吞吐=%5.0f/s  错误=%d（busy_timeout 兜住写锁竞争）\n",
			b.name, st.throughpt, st.errs)
		db.Close()
	}
	fmt.Println("  ⇒ 池从 2 开到 8，写吞吐不升反降——SQLite 单写者面前，大池只会加剧锁竞争。")
}
