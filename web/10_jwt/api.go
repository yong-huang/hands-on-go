package main

// api.go：令牌端点 + Bearer 校验。
// POST /api/v1/auth/token  凭证换令牌（凭证校验为占位，真实版=实验 09 的 argon2id）
// POST /api/v1/auth/refresh 刷新令牌
// GET  /api/v1/me          Bearer 校验通过返回当前用户

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func newAPI() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})

	// 占位凭证：真实 marklet 在实验 09 已接 argon2id，本篇不重复
	mux.HandleFunc("POST /api/v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, "need json {username, password}")
			return
		}
		if in.Username != "alice" || in.Password != "Passw0rd1" {
			writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
			return
		}
		token, err := Sign(in.Username, 15*time.Minute)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"token": token, "token_type": "Bearer"})
	})

	mux.HandleFunc("POST /api/v1/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		user, ok := bearer(w, r)
		if !ok {
			return
		}
		token, err := Refresh(mustRawToken(r), 15*time.Minute)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		_ = user
		writeJSON(w, http.StatusOK, map[string]string{"token": token, "token_type": "Bearer"})
	})

	mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		user, ok := bearer(w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"user": user})
	})

	return mux
}

// bearer：Authorization: Bearer <token> 解析 + 验证，失败统一 401。
func bearer(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := mustRawToken(r)
	user, err := Parse(raw)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return "", false
	}
	return user, true
}

func mustRawToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
