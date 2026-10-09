// 实验 09：argon2id 密码哈希与 Redis 会话——注册/登录/登出/当前用户。
//
//	第 1 步：盐随机性——同一密码两次哈希结果必然不同，但都能通过验证
//	第 2 步：弱密码策略——四类弱密码逐条拒绝，规则表可执行
//	第 3 步：会话固定防御——登录前后 SID 必须不同，旧 SID 立即作废
//	第 4 步：全流程走真服务——注册→登录→me→登出→me 401，Cookie 全程贯通
//
// 前置：Redis 容器（16379，建议带 AOF 持久化）：
//
//	docker run -d --name marklet-redis -p 16379:6379 redis:7-alpine redis-server --appendonly yes
//
// 运行方式：go run .（服务 127.0.0.1:19618，跑完自动退出）；断言见 go test -race。
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
)

const (
	redisAddr  = "127.0.0.1:16379"
	listenOn   = "127.0.0.1:19618"
	cookieName = "marklet_sid"
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func main() {
	demoPasswordAndPolicy()
	demoSessionRotation()
	demoHTTPFlow()
}

// demoPasswordAndPolicy：第 1、2 步（纯函数层，无 Redis）。
func demoPasswordAndPolicy() {
	fmt.Println("== 第 1 步：盐随机性——同密码两次哈希必然不同 ==")
	h1, err := HashPassword("correct horse battery staple")
	if err != nil {
		fatal(err)
	}
	h2, err := HashPassword("correct horse battery staple")
	if err != nil {
		fatal(err)
	}
	ok1, _ := VerifyPassword("correct horse battery staple", h1)
	ok2, _ := VerifyPassword("correct horse battery staple", h2)
	wrong, _ := VerifyPassword("wrong password", h1)
	fmt.Printf("  hash1 = %s...\n", h1[:46])
	fmt.Printf("  hash2 = %s...\n", h2[:46])
	fmt.Printf("  两次哈希相同 = %v；各自验证通过 = %v / %v；错误密码 = %v\n",
		h1 == h2, ok1, ok2, wrong)

	fmt.Println("== 第 2 步：弱密码策略——逐条拒绝 ==")
	for _, pw := range []string{"12345678", "abcdefgh", "a1", "Passw0rd1", "short1"} {
		violations := CheckPasswordPolicy(pw)
		if len(violations) == 0 {
			fmt.Printf("  %-14q → 通过\n", pw)
			continue
		}
		fmt.Printf("  %-14q → 拒绝：%s\n", pw, strings.Join(violations, "、"))
	}
}

// demoSessionRotation：第 3 步（Redis 会话层）。
func demoSessionRotation() {
	store, err := NewSessionStore(redisAddr)
	if err != nil {
		fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	fmt.Println("== 第 3 步：会话固定防御——登录轮换 ==")
	oldSID, err := store.Create(ctx, "anonymous", false)
	if err != nil {
		fatal(err)
	}
	newSID, err := store.Create(ctx, "alice", true)
	if err != nil {
		fatal(err)
	}
	if err := store.Destroy(ctx, oldSID); err != nil {
		fatal(err)
	}
	_, err = store.Get(ctx, oldSID)
	sess, err2 := store.Get(ctx, newSID)
	fmt.Printf("  登录前 SID %s...，登录后 %s...——必然不同\n", oldSID[:16], newSID[:16])
	fmt.Printf("  旧 SID 查询 → %v；新 SID → user=%s fixed=%v\n", err, sess.UserID, sess.Fixed)
	_ = err2
}

// demoHTTPFlow：第 4 步——起真服务，Cookie 贯通注册→登录→me→登出→me。
func demoHTTPFlow() {
	fmt.Println("== 第 4 步：全流程走真服务（Cookie 全程贯通）==")
	store, err := NewSessionStore(redisAddr)
	if err != nil {
		fatal(err)
	}
	defer store.Close()
	users := NewUsers()

	ln, err := net.Listen("tcp", listenOn)
	if err != nil {
		fatal(err)
	}
	srv := &http.Server{Handler: newAuthAPI(store, users), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	waitHealthy()

	client := &http.Client{}
	var jar []*http.Cookie // 手工管理 Cookie，让读者看清 SID 在头部怎么流动

	call := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, "http://"+listenOn+path, strings.NewReader(body))
		for _, ck := range jar {
			req.AddCookie(ck)
		}
		resp, err := client.Do(req)
		if err != nil {
			fatal(err)
		}
		defer resp.Body.Close()
		if set := resp.Cookies(); len(set) > 0 {
			jar = set // Set-Cookie 覆盖本地（含轮换后的新 SID）
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	code, body := call("POST", "/api/v1/auth/register", `{"username":"alice","password":"Passw0rd1"}`)
	fmt.Printf("  注册   → %d %s\n", code, strings.TrimSpace(body))
	code, _ = call("POST", "/api/v1/auth/login", `{"username":"alice","password":"Passw0rd1"}`)
	fmt.Printf("  登录   → %d（Set-Cookie 签发会话 SID）\n", code)
	code, body = call("GET", "/api/v1/auth/me", "")
	fmt.Printf("  me     → %d %s\n", code, strings.TrimSpace(body))
	code, _ = call("POST", "/api/v1/auth/logout", "")
	fmt.Printf("  登出   → %d（服务端会话已删）\n", code)
	code, body = call("GET", "/api/v1/auth/me", "")
	fmt.Printf("  me     → %d %s（SID 已作废）\n", code, strings.TrimSpace(body))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

// waitHealthy：轮询 /healthz。
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
