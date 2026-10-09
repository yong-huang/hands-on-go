package main

// middleware.go：安全中间件全家桶——安全响应头、Bearer 认证、CSRF 双提交、请求体上限。
//
// 四层各自回答一个攻击面：
//
//	SecureHeaders   浏览器侧的防御指令（CSP/XFO/nosniff/HSTS）——漏配一次，全线裸奔
//	Bearer          令牌无效/缺失一律 401（复用实验 10 的 JWT 校验，白名单仍在）
//	CSRF 双提交     写请求必须带 X-CSRF-Token，且与 Cookie 里的值恒定时间相等
//	MaxBody         请求体超限即 413——拒绝服务的第一道闸，先于业务读取
//
// 组装（声明序即执行序，洋葱模型见 net/12）：安全头最外，认证与 CSRF 在内，
// 体积限制贴着业务。豁免规则按路径判定：/healthz 全放行，/api/v1/csrf 免认证。

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// Middleware：包一层 handler 的函数（net/12 的定义）。
type Middleware func(http.Handler) http.Handler

// use：从内向外包裹——use(h, a, b, c) 的执行序 a→b→c→h，声明序即执行序。
func use(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

const csrfCookie = "marklet_csrf"

// SecureHeaders：安全响应头。逐条含义：
//   - Content-Security-Policy: default-src 'none'——API 不为任何脚本/样式背书
//   - X-Frame-Options: DENY——防点击劫持（页面被 iframe 套壳）
//   - X-Content-Type-Options: nosniff——禁止浏览器猜 Content-Type
//   - Strict-Transport-Security: HSTS——两年内强制 HTTPS（生产开关）
//   - Referrer-Policy——外链不泄露完整 URL
func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// Bearer：JWT 认证中间件。/healthz 与 /api/v1/csrf 豁免（探针与令牌发放点
// 必须可达）。失败统一 401 + WWW-Authenticate 头，错误细节不外泄。
func Bearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/api/v1/csrf" {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="marklet"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		raw := strings.TrimPrefix(auth, "Bearer ")
		claims := jwt.RegisteredClaims{}
		_, err := jwt.ParseWithClaims(raw, &claims,
			func(*jwt.Token) (any, error) { return []byte(jwtSecret), nil }, // 常量串须转 []byte，HMAC 只认字节
			jwt.WithValidMethods([]string{"HS256"}))
		if err != nil || claims.Subject == "" {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// 身份放进 context：request-scoped 数据的正规通道（net/12 的纪律）
		next.ServeHTTP(w, r.WithContext(withUser(r, claims.Subject)))
	})
}

// CSRFProtect：CSRF 双提交 cookie。写请求（非幂等方法）必须带 X-CSRF-Token 头，
// 与 csrf Cookie 恒定时间相等。攻击者站点能发跨站请求（Cookie 自动附带），
// 但读不到 Cookie 值（同源策略），补不出这个头。令牌发放只走 /api/v1/csrf。
// Cookie 不设 HttpOnly——双提交模式要求客户端可读它（本 API 场景由 /csrf 端点
// 在响应体里同步给出，非浏览器客户端无需碰 Cookie）。
func CSRFProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			// 幂等方法不校验；令牌发放只在 /api/v1/csrf（单一出口）
		default:
			ck, err := r.Cookie(csrfCookie)
			hdr := r.Header.Get("X-CSRF-Token")
			if err != nil || ck.Value == "" || hdr == "" ||
				subtle.ConstantTimeCompare([]byte(hdr), []byte(ck.Value)) != 1 {
				http.Error(w, "csrf check failed", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// MaxBody：请求体上限。MaxBytesReader 超限时传输被腰斩、handler 读取报错——
// 闸门在读取路径上，业务还没开始跑就已经被拒。
func MaxBody(n int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- CSRF Cookie 与用户 context ----------

// csrfToken：从请求里取（已种下的）CSRF 令牌；没有则现种并返回。
func csrfToken(w http.ResponseWriter, r *http.Request) string {
	if ck, err := r.Cookie(csrfCookie); err == nil && ck.Value != "" {
		return ck.Value
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	token := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: token, Path: "/", SameSite: http.SameSiteLaxMode,
	})
	return token
}

type ctxKey string

const userKey ctxKey = "user"

func withUser(r *http.Request, user string) context.Context {
	return context.WithValue(r.Context(), userKey, user)
}

// UserFrom：业务 handler 从 context 取认证身份（Bearer 中间件写入）。
func UserFrom(r *http.Request) string {
	v, _ := r.Context().Value(userKey).(string)
	return v
}
