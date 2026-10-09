package main

// helpers.go：建库、中间件组合、用户 context、响应助手。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
)

const pgHost = "postgres://marklet:marklet@127.0.0.1:15432"

// withDB：拼指定数据库的 DSN。
func withDB(name string) string { return pgHost + "/" + name + "?sslmode=disable" }

// EnsurePGDatabase：幂等创建实验数据库（连 postgres 系统库探查后建）。
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

// use：从内向外包裹（声明序即执行序，net/12 的洋葱模型）。
func use(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type ctxKey string

const uidKey ctxKey = "uid"

func withUID(r *http.Request, uid int64) context.Context {
	return context.WithValue(r.Context(), uidKey, uid)
}

// mustUserID：受保护组内取认证身份（Bearer 中间件保证存在）。
func mustUserID(r *http.Request) int64 {
	v, _ := r.Context().Value(uidKey).(int64)
	return v
}

// parseID：路径参数 {id}，非法直接 400。
func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "id must be positive integer")
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
