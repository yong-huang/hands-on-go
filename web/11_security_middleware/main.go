// 实验 11：安全中间件全家桶——四层防御的实测。
//
//	第 1 步：Bearer——无令牌 401（带 WWW-Authenticate）、伪造令牌 401、有效令牌 200
//	第 2 步：CSRF 双提交——写请求缺 X-CSRF-Token 403，补上 200
//	第 3 步：安全响应头——逐项打印（CSP/XFO/nosniff/HSTS/Referrer-Policy）
//	第 4 步：请求体上限——2MB 阈值，2MB+1 字节被 413 拒绝
//
// 组装：SecureHeaders → Bearer → CSRFProtect → MaxBody → 路由。
// 令牌签发复用实验 10 的 JWT（HS256 + 算法白名单）。
//
// 运行方式：go run .（服务 127.0.0.1:19620，跑完自动退出）；断言见 go test -race。
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	listenOn  = "127.0.0.1:19620"
	jwtSecret = "marklet-demo-secret-key-0123456789abcdef" // 实验 10 同款演示密钥
	maxBody   = int64(2 << 20)                             // 2MB
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// newSecureAPI：四层中间件包裹的路由。
func newSecureAPI() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})

	// CSRF 令牌发放点：种 Cookie 并在响应体里给出（非浏览器客户端用它回填请求头）
	mux.HandleFunc("GET /api/v1/csrf", func(w http.ResponseWriter, r *http.Request) {
		token := csrfToken(w, r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"csrf":%q}`, token)
	})

	mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		user := UserFrom(r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"user":%q}`, user)
	})

	mux.HandleFunc("POST /api/v1/transfer", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			// MaxBytesReader 的超限在这里现形：读取失败且 ResponseWriter 已标 413
			if strings.Contains(err.Error(), "request body too large") {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				fmt.Fprintln(w, "body too large")
				return
			}
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"user":%q,"received":%d}`, UserFrom(r), len(body))
	})

	return use(mux, SecureHeaders, Bearer, CSRFProtect, MaxBody(maxBody))
}

func main() {
	ln, err := net.Listen("tcp", listenOn)
	if err != nil {
		fatal(err)
	}
	srv := &http.Server{Handler: newSecureAPI(), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	waitHealthy()

	client := &http.Client{Timeout: 5 * time.Second}
	do := func(method, path, body string, hdr map[string]string) (int, http.Header, string) {
		req, _ := http.NewRequest(method, "http://"+listenOn+path, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(b)
	}

	// 有效令牌（实验 10 的签发函数）
	claims := jwt.RegisteredClaims{Subject: "alice"}
	good, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(jwtSecret))
	if err != nil {
		fatal(err)
	}
	forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).
		SignedString([]byte("attacker-key-00000000000000000000000000"))
	if err != nil {
		fatal(err)
	}

	fmt.Println("== 第 1 步：Bearer——三连 ==")
	code, hdr, body := do("GET", "/api/v1/me", "", nil)
	fmt.Printf("  无令牌   → %d（WWW-Authenticate=%q）\n", code, hdr.Get("WWW-Authenticate"))
	code, _, _ = do("GET", "/api/v1/me", "", map[string]string{"Authorization": "Bearer " + forged})
	fmt.Printf("  伪造令牌 → %d\n", code)
	code, _, body = do("GET", "/api/v1/me", "", map[string]string{"Authorization": "Bearer " + good})
	fmt.Printf("  有效令牌 → %d %s\n", code, body)

	fmt.Println("== 第 2 步：CSRF 双提交——写请求必须双证齐全 ==")
	// 先取 CSRF 令牌（GET 种 Cookie + 响应体给出）
	code, hdr, body = do("GET", "/api/v1/csrf", "", nil)
	cookie := strings.Split(hdr.Get("Set-Cookie"), ";")[0] // marklet_csrf=<token>
	csrf := body[strings.Index(body, `"`)+8 : len(body)-2]
	_, _ = code, hdr
	fmt.Printf("  令牌已发放（cookie=%s...）\n", csrf[:16])
	code, _, body = do("POST", "/api/v1/transfer", `{"amount":1}`,
		map[string]string{"Authorization": "Bearer " + good})
	fmt.Printf("  缺 CSRF 头 → %d %s\n", code, body)
	code, _, body = do("POST", "/api/v1/transfer", `{"amount":1}`, map[string]string{
		"Authorization": "Bearer " + good,
		"Cookie":        cookie,
		"X-CSRF-Token":  csrf,
	})
	fmt.Printf("  双证齐全   → %d %s\n", code, body)

	fmt.Println("== 第 3 步：安全响应头——逐项断言在测试里，这里打印 ==")
	code, hdr, _ = do("GET", "/healthz", "", nil)
	_ = code
	for _, k := range []string{"Content-Security-Policy", "X-Frame-Options",
		"X-Content-Type-Options", "Strict-Transport-Security", "Referrer-Policy"} {
		fmt.Printf("  %-28s = %s\n", k, hdr.Get(k))
	}

	fmt.Println("== 第 4 步：请求体上限——2MB 阈值 ==")
	code, _, body = do("POST", "/api/v1/transfer", strings.Repeat("x", 2<<20), map[string]string{
		"Authorization": "Bearer " + good,
		"Cookie":        cookie,
		"X-CSRF-Token":  csrf,
	})
	fmt.Printf("  恰好 2MB   → %d %s\n", code, body)
	code, _, body = do("POST", "/api/v1/transfer", strings.Repeat("x", 2<<20)+("x"), map[string]string{
		"Authorization": "Bearer " + good,
		"Cookie":        cookie,
		"X-CSRF-Token":  csrf,
	})
	fmt.Printf("  2MB+1 字节 → %d %s\n", code, body)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

func waitHealthy() {
	client := &http.Client{Timeout: time.Second}
	for i := 0; i < 100; i++ {
		if resp, err := client.Get("http://" + listenOn + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	fatal(fmt.Errorf("服务 2s 内未就绪"))
}
