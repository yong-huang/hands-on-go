package main

// store.go：同一个 Bookmarks 接口，SQLite 与 PostgreSQL 两个实现。
//
// 本篇的主角是接口多态：集成测试只认接口、只写一遍，两个实现各跑一遍。
// 两个实现都走 database/sql（SQLite 用 modernc 纯 Go 驱动，PG 用 pgx 的
// stdlib 适配），方言差异集中在两处——迁移 SQL 的方言版与 DSN 的连接参数。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // PG 驱动（database/sql 注册名 "pgx"）
	_ "modernc.org/sqlite"             // SQLite 驱动（注册名 "sqlite"）
)

// ErrNotFound：Get/Update 未命中时返回的哨兵错误。
var ErrNotFound = errors.New("bookmark not found")

// Bookmark：书签实体。
type Bookmark struct {
	ID        int64
	URL       string
	Title     string
	CreatedAt time.Time
}

// Bookmarks：存储契约。SQLite 与 PG 的实现共同遵守，调用方（测试、API）
// 对背后是哪个引擎无感——这就是"换数据库不动业务代码"的落点。
type Bookmarks interface {
	Migrate() ([]string, error)
	Insert(ctx context.Context, url, title string) (int64, error)
	Get(ctx context.Context, id int64) (*Bookmark, error)
	Update(ctx context.Context, id int64, title string) (bool, error)
	Delete(ctx context.Context, id int64) (bool, error)
	Close() error
}

// migrations：同一个版本号，两种方言各一份 SQL。
// 差异看 001：SQLite 自增主键写作 INTEGER PRIMARY KEY AUTOINCREMENT，
// PG 写作 BIGSERIAL——语义相同，方言不同，这正是迁移移植的全部工作量。
var migrations = []struct {
	name          string
	sqlite, pgsql string
}{
	{"001_create_bookmarks", `CREATE TABLE IF NOT EXISTS bookmarks (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		url        TEXT NOT NULL,
		title      TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`, `CREATE TABLE IF NOT EXISTS bookmarks (
		id         BIGSERIAL PRIMARY KEY,
		url        TEXT NOT NULL,
		title      TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`},
	{"002_index_url",
		`CREATE INDEX IF NOT EXISTS idx_bookmarks_url ON bookmarks(url)`,
		`CREATE INDEX IF NOT EXISTS idx_bookmarks_url ON bookmarks(url)`},
}

// sqlStore：两个方言共用的实现体。读写的 SQL 全是标准写法（? 或 $1 由
// 驱动各自处理），唯一需要分叉的是迁移的方言选择与时间列的扫描方式。
type sqlStore struct {
	db   *sql.DB
	kind string // "sqlite" | "postgres"
}

// OpenSQLite：本地文件库，WAL + busy_timeout（实验 02 的配置原样继承）。
func OpenSQLite(path string) (Bookmarks, error) {
	const pragmas = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?%s", path, pragmas))
	if err != nil {
		return nil, err
	}
	return &sqlStore{db: db, kind: "sqlite"}, nil
}

// OpenPG：连接 Docker 里的 marklet-pg（127.0.0.1:15432，见 pg.sh）。
func OpenPG(dsn string) (Bookmarks, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	// 连通性探活：sql.Open 是惰性的，Ping 一次才暴露"容器没起"这类问题
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w（容器没起？先跑 ./pg.sh start）", err)
	}
	return &sqlStore{db: db, kind: "postgres"}, nil
}

// Migrate：版本表迁移器（实验 02 的机制），方言差异由 kind 选择 SQL 列。
func (s *sqlStore) Migrate() ([]string, error) {
	idDDL := map[string]string{
		"sqlite": `CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name    TEXT NOT NULL,
			applied_at TEXT NOT NULL)`,
		"postgres": `CREATE TABLE IF NOT EXISTS schema_migrations (
			version BIGINT PRIMARY KEY,
			name    TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	}[s.kind]

	if _, err := s.db.Exec(idDDL); err != nil {
		return nil, err
	}

	var applied []string
	for i, m := range migrations {
		var n int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE version = $1`, i+1,
		).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			continue
		}
		ddl := m.sqlite
		if s.kind == "postgres" {
			ddl = m.pgsql
		}
		tx, err := s.db.Begin()
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ddl); err != nil {
			tx.Rollback()
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, name, applied_at)
			VALUES ($1, $2, $3)`, i+1, m.name, time.Now().UTC()); err != nil {
			tx.Rollback()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		applied = append(applied, m.name)
	}
	return applied, nil
}

func (s *sqlStore) Insert(ctx context.Context, url, title string) (int64, error) {
	var id int64
	var err error
	if s.kind == "sqlite" {
		// SQLite：LastInsertId 取自增列
		res, e := s.db.ExecContext(ctx,
			`INSERT INTO bookmarks (url, title, created_at) VALUES (?, ?, ?)`,
			url, title, time.Now().UTC().Format(time.RFC3339))
		if e != nil {
			return 0, e
		}
		return res.LastInsertId()
	}
	// PG：标准做法是 RETURNING，同一条语句把新 id 带回来
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO bookmarks (url, title) VALUES ($1, $2) RETURNING id`,
		url, title).Scan(&id)
	return id, err
}

func (s *sqlStore) Get(ctx context.Context, id int64) (*Bookmark, error) {
	var b Bookmark
	var err error
	if s.kind == "sqlite" {
		var created string
		err = s.db.QueryRowContext(ctx,
			`SELECT id, url, title, created_at FROM bookmarks WHERE id = ?`, id).
			Scan(&b.ID, &b.URL, &b.Title, &created)
		if err == nil {
			b.CreatedAt, err = time.Parse(time.RFC3339, created)
		}
	} else {
		// PG 的 TIMESTAMPTZ 直接扫进 time.Time，无需字符串中转
		err = s.db.QueryRowContext(ctx,
			`SELECT id, url, title, created_at FROM bookmarks WHERE id = $1`, id).
			Scan(&b.ID, &b.URL, &b.Title, &b.CreatedAt)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *sqlStore) Update(ctx context.Context, id int64, title string) (bool, error) {
	var res sql.Result
	var err error
	if s.kind == "sqlite" {
		res, err = s.db.ExecContext(ctx,
			`UPDATE bookmarks SET title = ? WHERE id = ?`, title, id)
	} else {
		res, err = s.db.ExecContext(ctx,
			`UPDATE bookmarks SET title = $1 WHERE id = $2`, title, id)
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *sqlStore) Delete(ctx context.Context, id int64) (bool, error) {
	var res sql.Result
	var err error
	if s.kind == "sqlite" {
		res, err = s.db.ExecContext(ctx, `DELETE FROM bookmarks WHERE id = ?`, id)
	} else {
		res, err = s.db.ExecContext(ctx, `DELETE FROM bookmarks WHERE id = $1`, id)
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *sqlStore) Close() error { return s.db.Close() }

// Kind：方言名（演示与测试的展示用）。
func (s *sqlStore) Kind() string { return s.kind }
