// marklet 容器入口：健康检查 + 书签最小 CRUD（PG，启动即迁移）。
// 镜像构建的验证载体——完整功能在 web/01~18 各篇。
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := os.Getenv("MARKLET_PG_DSN")
	if dsn == "" {
		dsn = "postgres://marklet:marklet@localhost:5432/marklet?sslmode=disable"
	}
	var db *sql.DB
	var err error
	for i := 0; i < 30; i++ { // 重试连接：compose 的健康条件到应用连接之间有缝隙
		if db, err = sql.Open("pgx", dsn); err == nil {
			if err = db.Ping(); err == nil {
				break
			}
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pg 不可用:", err)
		os.Exit(1)
	}
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS bookmarks (
			id BIGSERIAL PRIMARY KEY,
			url TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			fmt.Fprintln(os.Stderr, "迁移失败:", err)
			os.Exit(1)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /api/v1/bookmarks", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ URL, Title string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.URL == "" {
			http.Error(w, "need {url}", http.StatusBadRequest)
			return
		}
		var id int64
		if err := db.QueryRowContext(r.Context(),
			`INSERT INTO bookmarks (url, title) VALUES ($1, $2) RETURNING id`,
			in.URL, in.Title).Scan(&id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%d}`, id)
	})
	mux.HandleFunc("GET /api/v1/bookmarks", func(w http.ResponseWriter, _ *http.Request) {
		rows, err := db.Query(`SELECT id, url, title FROM bookmarks ORDER BY id`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id int64
			var url, title string
			_ = rows.Scan(&id, &url, &title)
			out = append(out, map[string]any{"id": id, "url": url, "title": title})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	fmt.Println("marklet listening on", os.Getenv("MARKLET_ADDR"))
	if err := http.ListenAndServe(os.Getenv("MARKLET_ADDR"), mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
