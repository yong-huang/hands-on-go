package main

// api.go：四个认证端点——注册、登录、登出、当前用户。
// 登录轮换（会话固定防御）的实现落在 login handler：
// 只要请求带来了旧 SID（无论匿名会话还是已过期的登录态），一律销毁、签发新 SID。

import (
	"encoding/json"
	"errors"
	"net/http"
)

// newAuthAPI：认证路由。Cookie 属性是安全默认：
// HttpOnly（JS 读不到，防 XSS 偷 SID）、SameSite=Lax（防大部分 CSRF）、
// Path=/。Secure 属性留给 HTTPS 部署（实验 19）时开启。
func newAuthAPI(store *SessionStore, users *Users) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("POST /api/v1/auth/register", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Username == "" {
			writeErr(w, http.StatusBadRequest, "need json {username, password}")
			return
		}
		if v := CheckPasswordPolicy(in.Password); len(v) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "弱密码", "violations": v})
			return
		}
		if err := users.Register(in.Username, in.Password); err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"status": "registered"})
	})

	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, "need json {username, password}")
			return
		}
		ok, err := users.Verify(in.Username, in.Password)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			// 统一文案：不区分"用户不存在"与"密码错误"，不给枚举空间
			writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
			return
		}
		// 会话固定防御：登录即轮换——旧 SID（若有）销毁，签发全新 SID
		if old, err := r.Cookie(cookieName); err == nil && old.Value != "" {
			_ = store.Destroy(r.Context(), old.Value)
		}
		sid, err := store.Create(r.Context(), in.Username, true)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: cookieName, Value: sid, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "logged in"})
	})

	mux.HandleFunc("POST /api/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if ck, err := r.Cookie(cookieName); err == nil {
			_ = store.Destroy(r.Context(), ck.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name: cookieName, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
	})

	mux.HandleFunc("GET /api/v1/auth/me", func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie(cookieName)
		if err != nil || ck.Value == "" {
			writeErr(w, http.StatusUnauthorized, "未登录")
			return
		}
		sess, err := store.Get(r.Context(), ck.Value)
		if errors.Is(err, ErrNoSession) {
			writeErr(w, http.StatusUnauthorized, "会话无效或已过期")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"user": sess.UserID, "login_at": sess.LoginAt, "rotated": sess.Fixed,
		})
	})

	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
