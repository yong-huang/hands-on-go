package main

// store.go：marklet 数据层完整版——书签 CRUD + 事务助手 + 余额演示表。
//
// 相对实验 02 的增长：
//   - 迁移列表新增 003_add_counters（转账式实验的计数表）——迁移列表长出新版本，
//     老库补跑一次即可，这正是版本表迁移器的日常
//   - DSN 增加 _txlock=immediate：事务从 BEGIN 就取写锁，读-改-写整体串行化
//   - Update（CRUD 补齐）与 WithTx（事务边界助手）
//   - balanceDecrement：同一业务的三种写法，对应实验 1/2/3 步的对照

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound：Get/Update 未命中时返回的哨兵错误。
var ErrNotFound = errors.New("bookmark not found")

// Bookmark：书签实体。
type Bookmark struct {
	ID        int64     `json:"id"`
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

// migrations：003 是新版本——老库补跑一次就能长出新表。
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
	{"003_add_counters", `CREATE TABLE IF NOT EXISTS counters (
		id INTEGER PRIMARY KEY,
		n  INTEGER NOT NULL
	)`},
}

// Store：书签存储 + 事务边界。
// _txlock=immediate 让 BeginTx 产出 BEGIN IMMEDIATE——事务一开就取写锁，
// 后续的读-改-写作为整体被串行化（这是 SQLite 防丢更新的关键开关）。
type Store struct {
	db   *sql.DB
	ins  *sql.Stmt
	get  *sql.Stmt
	list *sql.Stmt
	del  *sql.Stmt
}

// OpenStore：打开连接。PRAGMA 三连与实验 02 相同，另加 _txlock=immediate。
func OpenStore(path string) (*Store, error) {
	const pragmas = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?%s", path, pragmas))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	return &Store{db: db}, nil
}

// DB：暴露底层连接池（事务实验直接在池上开事务）。
func (s *Store) DB() *sql.DB { return s.db }

// Migrate：版本表迁移器（与实验 02 同机制），首次调用后备好 CRUD 预编译语句。
func (s *Store) Migrate() ([]string, error) {
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
			continue
		}
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

	if s.ins == nil {
		if err := s.prepare(); err != nil {
			return nil, err
		}
	}
	return applied, nil
}

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

// WithTx：事务边界助手——fn 返回 nil 则提交，否则回滚。
// defer Rollback 看似多余，实际是兜底：Commit 之后它变成空操作，
// 而 fn 中途 return 或 panic 时它保证不留下悬挂事务。
func WithTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------- CRUD ----------

// Insert：新增书签。
func (s *Store) Insert(ctx context.Context, url, title string) (int64, error) {
	res, err := s.ins.ExecContext(ctx, url, title, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Get：按 id 查询。
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

// Update：改标题，返回是否真的改了（id 不存在 = false）。
func (s *Store) Update(ctx context.Context, id int64, title string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE bookmarks SET title = ? WHERE id = ?`, title, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// List：全量列表，id 倒序。
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

// Delete：按 id 删除。
func (s *Store) Delete(ctx context.Context, id int64) (bool, error) {
	res, err := s.del.ExecContext(ctx, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Close：释放预编译语句并关池。
func (s *Store) Close() error {
	for _, st := range []*sql.Stmt{s.ins, s.get, s.list, s.del} {
		if st != nil {
			st.Close()
		}
	}
	return s.db.Close()
}

// ---------- 转账式实验的三种写法（同一业务：把余额减 1） ----------

// seedBalance：初始化余额表，返回 id。
func (s *Store) seedBalance(ctx context.Context, n int) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO counters (id, n) VALUES (1, ?)`, n)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// readBalance：读当前余额。
func (s *Store) readBalance(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT n FROM counters WHERE id = 1`).Scan(&n)
	return n, err
}

// decrementRacy：无事务的读-改-写——把读到的值在 Go 里减 1，再整体写回。
// 读和写是两条独立语句，中间的业务窗口（delay 模拟）就是丢更新的温床。
func (s *Store) decrementRacy(ctx context.Context, delay time.Duration) error {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT n FROM counters WHERE id = 1`).Scan(&n); err != nil {
		return err
	}
	time.Sleep(delay) // 业务处理窗口：读到的 n 在这里悄悄过期
	_, err := s.db.ExecContext(ctx,
		`UPDATE counters SET n = ? WHERE id = 1`, n-1)
	return err
}

// decrementTx：事务版读-改-写——同样的业务逻辑包进 WithTx。
// _txlock=immediate 让事务开始就持有写锁，并发的整个读-改-写被串行化，
// 中间窗口里不会有第二个人读到过期值。
func (s *Store) decrementTx(ctx context.Context, delay time.Duration) error {
	return WithTx(ctx, s.db, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT n FROM counters WHERE id = 1`).Scan(&n); err != nil {
			return err
		}
		time.Sleep(delay)
		_, err := tx.ExecContext(ctx,
			`UPDATE counters SET n = ? WHERE id = 1`, n-1)
		return err
	})
}

// decrementAtomic：原子版——自减在一条 UPDATE 里完成（n = n - 1），
// 不给"读到过期值"留窗口。不需要事务，也是最该首选的写法。
func (s *Store) decrementAtomic(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE counters SET n = n - 1 WHERE id = 1`)
	return err
}
