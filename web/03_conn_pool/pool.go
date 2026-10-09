package main

// pool.go：连接池参数实验台——池配置、压测工作负载、DBStats 计量。
//
// database/sql 的连接池有三个主旋钮：
//
//	SetMaxOpenConns(n)   同时最多 n 条连接，多出的请求在池的取用队列里排队
//	SetMaxIdleConns(n)   空闲时最多保留 n 条，多余的关闭（0 = 不留空闲，每请求重开）
//	SetConnMaxLifetime(d) 连接最长存活 d，到期回收（防陈旧连接/让服务端换血）
//
// 本篇在 SQLite 上把三个旋钮拧到不同档位，用同一负载量出差距。

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动
)

// PoolConfig：池的三档旋钮。MaxOpen=0 表示不限制（database/sql 默认）。
type PoolConfig struct {
	MaxOpen  int
	MaxIdle  int
	Lifetime time.Duration
}

// openPool：按 DSN PRAGMA 开库（WAL + busy_timeout，与实验 02 同配置），
// 再把池旋钮拧到指定档位。
func openPool(path string, cfg PoolConfig) (*sql.DB, error) {
	const pragmas = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?%s", path, pragmas))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(cfg.MaxOpen)
	db.SetMaxIdleConns(cfg.MaxIdle)
	db.SetConnMaxLifetime(cfg.Lifetime)
	return db, nil
}

// mustSchema：建演示用的书签表（实验 03 只关心池行为，表结构从简）。
func mustSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS bookmarks (
		id    INTEGER PRIMARY KEY AUTOINCREMENT,
		url   TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT ''
	)`)
	return err
}

// seed：预置 n 行数据，读负载才有东西可查。单连接顺序写，最快。
func seed(db *sql.DB, n int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		if _, err := tx.Exec(`INSERT INTO bookmarks (url, title)
			VALUES (?, ?)`,
			fmt.Sprintf("https://example.com/page/%d", i), "种子数据"); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// stats：一轮压测的计量结果。
type stats struct {
	ops       int           // 总操作数
	errs      int           // 失败数
	p50       time.Duration // 一半请求快于此值
	p99       time.Duration // 99% 请求快于此值（尾巴延迟看它）
	throughpt float64       // 每秒完成数
	waitCount int64         // DBStats.WaitCount 增量：有多少次请求在池队列里等过
	waitDur   time.Duration // DBStats.WaitDuration 增量：累计等了多久
}

// snapshot：池等待计数器的读数（WaitCount 只增不减，做差即本轮增量）。
type snapshot struct {
	waitCount int64
	waitDur   time.Duration
}

func snap(db *sql.DB) snapshot {
	s := db.Stats()
	return snapshot{waitCount: s.WaitCount, waitDur: s.WaitDuration}
}

// runLoad：workers 个 goroutine 各跑 perWorker 次 op，量出 stats。
// op 由调用方注入（读或写），计量逻辑共用。
func runLoad(db *sql.DB, workers, perWorker int, op func(*sql.DB) error) stats {
	st := stats{ops: workers * perWorker}
	durations := make([]time.Duration, 0, st.ops)
	var mu sync.Mutex
	errs := make(chan error, st.ops)

	before := snap(db)
	start := time.Now()

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				t0 := time.Now()
				err := op(db)
				d := time.Since(t0)
				mu.Lock()
				durations = append(durations, d)
				mu.Unlock()
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	elapsed := time.Since(start)

	for range errs {
		st.errs++
	}
	after := snap(db)
	st.waitCount = after.waitCount - before.waitCount
	st.waitDur = after.waitDur - before.waitDur

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	st.p50 = durations[len(durations)*50/100]
	st.p99 = durations[len(durations)*99/100]
	st.throughpt = float64(st.ops) / elapsed.Seconds()
	st.ops = len(durations)
	return st
}

// runRead：读负载——一条预编译 SELECT，纯读无写锁，池是唯一排队点。
func runRead(db *sql.DB, workers, perWorker int) (stats, error) {
	stmt, err := db.Prepare(`SELECT id, url FROM bookmarks ORDER BY id LIMIT 5`)
	if err != nil {
		return stats{}, err
	}
	defer stmt.Close()

	ctx := context.Background()
	st := runLoad(db, workers, perWorker, func(db *sql.DB) error {
		rows, err := stmt.QueryContext(ctx)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var url string
			if err := rows.Scan(&id, &url); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	return st, nil
}

// runWrite：写负载——插入。SQLite 单写者，写锁竞争叠加在池排队之上。
func runWrite(db *sql.DB, workers, perWorker int) (stats, error) {
	ctx := context.Background()
	st := runLoad(db, workers, perWorker, func(db *sql.DB) error {
		_, err := db.ExecContext(ctx,
			`INSERT INTO bookmarks (url, title) VALUES (?, ?)`, "并发写", "03")
		return err
	})
	return st, nil
}
