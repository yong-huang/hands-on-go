package main

// store.go：用户表 + 按 owner 隔离的书签表（PG）。
//
// 本篇的数据层增量：
//   - users 表：argon2id PHC 串落库（实验 09 的哈希 + 实验 05 的 PG 迁移汇合）
//   - bookmarks.owner_id：每一行书签都归属一个用户——数据隔离的根基
//   - 所有查询带 owner 过滤：IDOR 防御的正确位置在 SQL 的 WHERE 里，
//     而不是 handler 里"查出来再比对"（后者在并发与缓存下处处漏）

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/argon2"
)

// ErrNotFound：未命中（含"不归你"）。IDOR 防御的关键：两者同语义同响应。
var ErrNotFound = errors.New("not found")

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id           BIGSERIAL PRIMARY KEY,
		username     TEXT NOT NULL UNIQUE,
		password_phc TEXT NOT NULL,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE TABLE IF NOT EXISTS bookmarks (
		id         BIGSERIAL PRIMARY KEY,
		owner_id   BIGINT NOT NULL REFERENCES users(id),
		url        TEXT NOT NULL,
		title      TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_bookmarks_owner ON bookmarks(owner_id)`,
}

// EnsureSchema：幂等建表（本篇无版本表——表结构在此后实验中稳定，
// 版本表迁移器的完整形态见实验 02/05）。
func EnsureSchema(ctx context.Context, db *sql.DB) error {
	for _, ddl := range migrations {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	return nil
}

// OpenDB：连库 + 探活。
func OpenDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping: %w（容器没起？../05_pg_migration/pg.sh start）", err)
	}
	return db, nil
}

// ---------- 用户 ----------

// CreateUser：注册。argon2id PHC 串落库（实验 09 的哈希 + 实验 05 的 PG 汇合点）。
func CreateUser(ctx context.Context, db *sql.DB, username, password string) error {
	if len(password) < 8 {
		return errors.New("密码至少 8 位")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash := argon2.IDKey([]byte(password), salt, 2, 32*1024, 2, 32)
	phc := fmt.Sprintf("$argon2id$v=%d$m=32768,t=2,p=2$%s$%s",
		argon2.Version, base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash))
	_, err := db.ExecContext(ctx,
		`INSERT INTO users (username, password_phc) VALUES ($1, $2)`, username, phc)
	return err
}

// VerifyUser：登录校验，返回用户 id。用户不存在也做一次哈希（拉平耗时防枚举）。
func VerifyUser(ctx context.Context, db *sql.DB, username, password string) (int64, bool, error) {
	var phc string
	err := db.QueryRowContext(ctx,
		`SELECT password_phc FROM users WHERE username = $1`, username).Scan(&phc)
	if errors.Is(err, sql.ErrNoRows) {
		argon2.IDKey([]byte(password), make([]byte, 16), 2, 32*1024, 2, 32)
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	ok, err := verifyPHC(password, phc)
	if err != nil || !ok {
		return 0, false, err
	}
	var id int64
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM users WHERE username = $1`, username).Scan(&id); err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// verifyPHC：解析 PHC 串 → 重算 → 恒定时间比较（实验 09 同款）。
func verifyPHC(password, phc string) (bool, error) {
	seg := strings.SplitN(phc, "$", 6) // ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(seg) != 6 || seg[1] != "argon2id" {
		return false, errors.New("malformed hash")
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(seg[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(seg[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(seg[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// ---------- 书签（全部按 owner 过滤） ----------

type Bookmark struct {
	ID    int64  `json:"id"`
	Owner int64  `json:"owner"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// ListBookmarks：只返回 owner 的书签。
func ListBookmarks(ctx context.Context, db *sql.DB, owner int64) ([]Bookmark, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, owner_id, url, title FROM bookmarks WHERE owner_id = $1 ORDER BY id DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bookmark
	for rows.Next() {
		var b Bookmark
		if err := rows.Scan(&b.ID, &b.Owner, &b.URL, &b.Title); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CreateBookmark：归属当前用户。
func CreateBookmark(ctx context.Context, db *sql.DB, owner int64, url, title string) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx,
		`INSERT INTO bookmarks (owner_id, url, title) VALUES ($1, $2, $3) RETURNING id`,
		owner, url, title).Scan(&id)
	return id, err
}

// getBookmark：owner 过滤的 SELECT——别人的书签 = 不存在。
func getBookmark(ctx context.Context, db *sql.DB, owner, id int64) (*Bookmark, error) {
	var b Bookmark
	err := db.QueryRowContext(ctx,
		`SELECT id, owner_id, url, title FROM bookmarks WHERE id = $1 AND owner_id = $2`,
		id, owner).Scan(&b.ID, &b.Owner, &b.URL, &b.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &b, err
}

func GetBookmark(ctx context.Context, db *sql.DB, owner, id int64) (*Bookmark, error) {
	return getBookmark(ctx, db, owner, id)
}

func UpdateBookmark(ctx context.Context, db *sql.DB, owner, id int64, title string) (bool, error) {
	res, err := db.ExecContext(ctx,
		`UPDATE bookmarks SET title = $1 WHERE id = $2 AND owner_id = $3`, title, id, owner)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func DeleteBookmark(ctx context.Context, db *sql.DB, owner, id int64) (bool, error) {
	res, err := db.ExecContext(ctx,
		`DELETE FROM bookmarks WHERE id = $1 AND owner_id = $2`, id, owner)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
