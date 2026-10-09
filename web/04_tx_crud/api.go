package main

// api.go：CRUD 全量接线——Store 的四个方法挂上 HTTP 路由。
//
// 事务边界与 handler 的关系：marklet 目前的每条路由都是单语句写，
// 单条 SQL 自身就是原子的，handler 不需要显式事务；事务边界留在 store 层
// （迁移器的 DDL+版本记录、转账式 decrementTx）。等一条请求需要改多张表时，
// 才把 WithTx 提到 handler 层。

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// newAPI：完整路由表。方法路由语法见实验 01；{id} 是 Go 1.22 的路径参数，
// handler 里用 r.PathValue("id") 取。
func newAPI(s *Store) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("POST /api/v1/bookmarks", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ URL, Title string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.URL == "" {
			writeErr(w, http.StatusBadRequest, "need json {url, title}")
			return
		}
		id, err := s.Insert(r.Context(), in.URL, in.Title)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
	})

	mux.HandleFunc("GET /api/v1/bookmarks", func(w http.ResponseWriter, r *http.Request) {
		all, err := s.List(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"bookmarks": all})
	})

	mux.HandleFunc("GET /api/v1/bookmarks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r)
		if !ok {
			return
		}
		b, err := s.Get(r.Context(), id)
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "no such bookmark")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, b)
	})

	mux.HandleFunc("PUT /api/v1/bookmarks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r)
		if !ok {
			return
		}
		var in struct{ Title string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, "need json {title}")
			return
		}
		ok, err := s.Update(r.Context(), id, in.Title)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			writeErr(w, http.StatusNotFound, "no such bookmark")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("DELETE /api/v1/bookmarks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r)
		if !ok {
			return
		}
		deleted, err := s.Delete(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !deleted {
			writeErr(w, http.StatusNotFound, "no such bookmark")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}

// parseID：解析路径参数 {id}，非法时直接写 400。
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
