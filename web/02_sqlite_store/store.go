package main

// store.go：SQLite 持久层——连接 PRAGMA、手写迁移器、书签 CRUD。
//
// 驱动选 modernc.org/sqlite：纯 Go 实现（无 CGO），go test -race 与交叉编译
// 都不需要 C 工具链。database/sql 只认驱动注册名 "sqlite"。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // 注册 database/sql 驱动，匿名导入
)

// ErrNotFound：Get 未命中时返回的哨兵错误，调用方用 errors.Is 判定。
var ErrNotFound = errors.New("bookmark not found")

// Bookmark：书签实体，marklet 的核心领域对象。
type Bookmark struct {
	ID        int64
	URL       string
	Title     string
	CreatedAt time.Time
}

// migrations：按序执行的迁移。每个版本只执行一次，执行记录落在
// schema_migrations 表——"迁移器"的全部秘密就是这张版本表。
// SQLite 支持事务性 DDL：CREATE TABLE 也放进事务，失败即整体回滚。
var migrations = []struct {
	name string
	stmt string
}{
	{"001_create_bookmarks", `CREATE TABLE IF NOT EXISTS bookmarks (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		url        TEXT NOT NULL,
		title      TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`},
	{"002_index_url", `CREATE INDEX IF NOT EXISTS idx_bookmarks_url ON bookmarks(url)`},
}

// Store：书签存储。预编译语句在首次 Migrate 完成后备好（表不存在时
// Prepare 会报 "no such table"，所以准备动作必须排在建表之后）。
type Store struct {
	db       *sql.DB
	prepared bool
	ins      *sql.Stmt
	get      *sql.Stmt
	list     *sql.Stmt
	del      *sql.Stmt
}

// OpenStore：打开连接并设置三个 PRAGMA（SQLite 的运行时开关）：
//   - journal_mode(WAL)：预写日志模式，写不再阻塞读（默认回滚日志下写独占）
//   - busy_timeout(5000)：撞锁时等 5s 再报错，而不是立刻返回 SQLITE_BUSY
//   - foreign_keys(1)：SQLite 默认关闭外键约束，按 SQL 标准显式打开
//
// PRAGMA 通过 DSN 传入，连接池里每条新连接都会自动执行。
func OpenStore(path string) (*Store, error) {
	const pragmas = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?%s", path, pragmas))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	return &Store{db: db}, nil
}

// Migrate：顺序执行未应用的迁移，返回本次实际执行的版本名（幂等：
// 已应用的版本直接跳过，所以连跑两次第二次必然是空列表）。
// 首次调用完成后会顺带备好 CRUD 预编译语句——schema 就绪是它们的前提。
func (s *Store) Migrate() ([]string, error) {
	// 版本表自身也是迁移的一部分，但要用"不存在才建"的方式自举
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return nil, err
	}

	var applied []string
	for i, m := range migrations {
		var n int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, i+1,
		).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			continue // 已应用，跳过——这就是幂等
		}

		// DDL 与版本记录同事务：要么都成，要么都不成（SQLite 支持事务性 DDL）
		tx, err := s.db.Begin()
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(m.stmt); err != nil {
			tx.Rollback()
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at)
			VALUES (?, ?, ?)`, i+1, m.name, time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		applied = append(applied, m.name)
	}

	if !s.prepared {
		if err := s.prepare(); err != nil {
			return nil, err
		}
		s.prepared = true
	}
	return applied, nil
}

// prepare：编译 CRUD 预编译语句，热路径省去每次解析 SQL 的开销。
func (s *Store) prepare() error {
	var err error
	if s.ins, err = s.db.Prepare(`INSERT INTO bookmarks (url, title, created_at)
		VALUES (?, ?, ?)`); err != nil {
		return err
	}
	if s.get, err = s.db.Prepare(`SELECT id, url, title, created_at
		FROM bookmarks WHERE id = ?`); err != nil {
		return err
	}
	if s.list, err = s.db.Prepare(`SELECT id, url, title, created_at
		FROM bookmarks ORDER BY id DESC`); err != nil {
		return err
	}
	if s.del, err = s.db.Prepare(`DELETE FROM bookmarks WHERE id = ?`); err != nil {
		return err
	}
	return nil
}

// Insert：新增书签，返回数据库分配的 id。
// 参数一律走 ? 占位符——SQL 注入防线的正确位置在驱动参数化，不在手工转义。
func (s *Store) Insert(ctx context.Context, url, title string) (int64, error) {
	res, err := s.ins.ExecContext(ctx, url, title, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Get：按 id 查询单条；未命中返回 ErrNotFound。
func (s *Store) Get(ctx context.Context, id int64) (*Bookmark, error) {
	var b Bookmark
	var created string
	err := s.get.QueryRowContext(ctx, id).Scan(&b.ID, &b.URL, &b.Title, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if b.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
		return nil, err
	}
	return &b, nil
}

// List：全量列表，id 倒序（新书签在前）。
func (s *Store) List(ctx context.Context) ([]Bookmark, error) {
	rows, err := s.list.QueryContext(ctx)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Bookmark
	for rows.Next() {
		var b Bookmark
		var created string
		if err := rows.Scan(&b.ID, &b.URL, &b.Title, &created); err != nil {
			return nil, err
		}
		if b.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Delete：按 id 删除，返回是否真的删了（id 不存在时 affected 为 0）。
func (s *Store) Delete(ctx context.Context, id int64) (bool, error) {
	res, err := s.del.ExecContext(ctx, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Close：先释放预编译语句，再关连接池——顺序反了会得到 "busy" 类错误。
func (s *Store) Close() error {
	if s.prepared {
		for _, st := range []*sql.Stmt{s.ins, s.get, s.list, s.del} {
			st.Close()
		}
	}
	return s.db.Close()
}
