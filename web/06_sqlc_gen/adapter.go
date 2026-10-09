package main

// adapter.go：两条路到同一个 PG——手写 SQL 与 sqlc 生成码，适配成同一接口。
//
//	manualStore：database/sql + 手写 SQL（实验 05 的实现原样平移）
//	sqlcStore：  pgx 原生连接 + gen/ 目录里的生成码
//
// 两者的方法签名完全一致——本篇的对照实验就建立在"接口相同、来路不同"上。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	pgx "github.com/jackc/pgx/v5"      // pgx 原生连接（生成码路）
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql 注册名 "pgx"（手写路）

	gen "marklet/06_sqlc_gen/gen" // sqlc 生成码（包名 db，别名 gen）
)

// ErrNotFound：未命中的哨兵错误，两个实现共用。
var ErrNotFound = errors.New("bookmark not found")

// Bookmark：书签实体。
type Bookmark struct {
	ID        int64
	URL       string
	Title     string
	CreatedAt time.Time
}

// Bookmarks：存储契约（与实验 05 同形）。
type Bookmarks interface {
	Migrate() ([]string, error)
	Insert(ctx context.Context, url, title string) (int64, error)
	Get(ctx context.Context, id int64) (*Bookmark, error)
	Update(ctx context.Context, id int64, title string) (bool, error)
	Delete(ctx context.Context, id int64) (bool, error)
	Close() error
}

// pgHost：PG 容器地址；本篇用独立数据库 marklet06，与实验 05 的 marklet 库
// 互不踩踏（go test ./... 会并行跑两个包，共享一张表会互相 DROP）。
const pgHost = "postgres://marklet:marklet@127.0.0.1:15432"

// withDB：拼出指定数据库的 DSN。
func withDB(name string) string { return pgHost + "/" + name + "?sslmode=disable" }

// EnsurePGDatabase：幂等创建实验数据库（先连 postgres 系统库探查，缺则建）。
// 数据库名是常量、不走参数化——标识符不能进占位符，这里无注入面。
func EnsurePGDatabase(name string) error {
	admin, err := sql.Open("pgx", pgHost+"/postgres?sslmode=disable")
	if err != nil {
		return err
	}
	defer admin.Close()
	var n int
	if err := admin.QueryRow(
		`SELECT COUNT(*) FROM pg_database WHERE datname = $1`, name).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err = admin.Exec(`CREATE DATABASE ` + name)
	return err
}

// migrations：真实库的 schema 由它负责（与 schema/schema.sql 保持人工同步——
// 两份 schema 的漂移风险正是本篇要讨论的话题之一）。
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS bookmarks (
		id         BIGSERIAL PRIMARY KEY,
		url        TEXT NOT NULL,
		title      TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_bookmarks_url ON bookmarks(url)`,
}

// migrate：版本表迁移器（实验 05 的 PG 方言版，机制同实验 02）。
func migrate(ctx context.Context, db *sql.DB) ([]string, error) {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version BIGINT PRIMARY KEY, name TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return nil, err
	}
	var applied []string
	for i, ddl := range migrations {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE version = $1`, i+1).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			continue
		}
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return nil, err
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, i+1, "manual"); err != nil {
			return nil, err
		}
		applied = append(applied, fmt.Sprintf("%03d", i+1))
	}
	return applied, nil
}

// ---------- 手写路：database/sql ----------

type manualStore struct{ db *sql.DB }

// OpenManual：手写实现的构造函数。
func OpenManual(dsn string) (Bookmarks, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping: %w（容器没起？先跑 ../05_pg_migration/pg.sh start）", err)
	}
	return &manualStore{db: db}, nil
}

func (m *manualStore) Migrate() ([]string, error) {
	return migrate(context.Background(), m.db)
}

func (m *manualStore) Insert(ctx context.Context, url, title string) (int64, error) {
	var id int64
	err := m.db.QueryRowContext(ctx,
		`INSERT INTO bookmarks (url, title) VALUES ($1, $2) RETURNING id`,
		url, title).Scan(&id)
	return id, err
}

func (m *manualStore) Get(ctx context.Context, id int64) (*Bookmark, error) {
	var b Bookmark
	err := m.db.QueryRowContext(ctx,
		`SELECT id, url, title, created_at FROM bookmarks WHERE id = $1`, id).
		Scan(&b.ID, &b.URL, &b.Title, &b.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &b, err
}

func (m *manualStore) Update(ctx context.Context, id int64, title string) (bool, error) {
	res, err := m.db.ExecContext(ctx,
		`UPDATE bookmarks SET title = $1 WHERE id = $2`, title, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (m *manualStore) Delete(ctx context.Context, id int64) (bool, error) {
	res, err := m.db.ExecContext(ctx, `DELETE FROM bookmarks WHERE id = $1`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (m *manualStore) Close() error { return m.db.Close() }

// ---------- 生成码路：pgx + gen/db ----------

type sqlcStore struct {
	conn *pgx.Conn
	q    *gen.Queries
	db   *sql.DB // 只为复用同一个迁移器（生成码不管迁移）
}

// OpenSQLC：生成码实现的构造函数。
func OpenSQLC(dsn string) (*sqlcStore, error) {
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("pgx connect: %w（容器没起？先跑 ../05_pg_migration/pg.sh start）", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		conn.Close(context.Background())
		return nil, err
	}
	return &sqlcStore{conn: conn, q: gen.New(conn), db: db}, nil
}

func (s *sqlcStore) Migrate() ([]string, error) {
	return migrate(context.Background(), s.db)
}

func (s *sqlcStore) Insert(ctx context.Context, url, title string) (int64, error) {
	// 生成的 CreateBookmark：参数是结构体、返回是结构体——拼错字段名是编译错误
	row, err := s.q.CreateBookmark(ctx, gen.CreateBookmarkParams{Url: url, Title: title})
	if err != nil {
		return 0, err
	}
	return row.ID, nil
}

func (s *sqlcStore) Get(ctx context.Context, id int64) (*Bookmark, error) {
	row, err := s.q.GetBookmark(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &Bookmark{ID: row.ID, URL: row.Url, Title: row.Title, CreatedAt: row.CreatedAt}, nil
}

func (s *sqlcStore) Update(ctx context.Context, id int64, title string) (bool, error) {
	n, err := s.q.UpdateBookmarkTitle(ctx, gen.UpdateBookmarkTitleParams{ID: id, Title: title})
	return n > 0, err
}

func (s *sqlcStore) Delete(ctx context.Context, id int64) (bool, error) {
	n, err := s.q.DeleteBookmark(ctx, id)
	return n > 0, err
}

func (s *sqlcStore) Close() error {
	s.conn.Close(context.Background())
	return s.db.Close()
}
