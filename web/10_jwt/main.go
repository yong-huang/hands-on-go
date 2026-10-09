// 实验 10：JWT 签发、验证与陷阱复现。
//
//	第 1 步：签发与验证——token 的三段结构，payload 可读但改不得
//	第 2 步：三个陷阱逐个撞——过期令牌、篡改 payload、alg=none，全部被拒
//	第 3 步：刷新流程——旧令牌换新令牌；同时暴露无状态的真实边界（旧令牌仍有效）
//	第 4 步：Bearer 走真服务——/me 带令牌 200，不带 401
//
// 凭证说明：演示的用户校验是占位（真实凭证校验=实验 09 的 argon2id），
// 本篇主角是令牌本身的签发与验证。
//
// 运行方式：go run .（服务 127.0.0.1:19619，跑完自动退出）；断言见 go test -race。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const listenOn = "127.0.0.1:19619"

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func main() {
	demoSignAndTraps()
	demoHTTPFlow()
}

// demoSignAndTraps：第 1-3 步（纯函数层）。
func demoSignAndTraps() {
	fmt.Println("== 第 1 步：签发与验证——三段结构，payload 可读但改不得 ==")
	token, err := Sign("alice", time.Minute)
	if err != nil {
		fatal(err)
	}
	parts := strings.Split(token, ".")
	fmt.Printf("  header  = %s\n", parts[0])
	fmt.Printf("  payload = %s（base64 解码即见 sub/exp——JWT 不是加密）\n", parts[1])
	if user, err := Parse(token); err != nil || user != "alice" {
		fatal(err)
	}
	fmt.Printf("  签名    = %s...（HMAC-SHA256，改一个字节就对不上）\n", parts[2][:24])
	fmt.Printf("  正常解析 → user=%s\n", mustUser(token))

	fmt.Println("== 第 2 步：三个陷阱逐个撞，全部被拒 ==")
	expired, err := Sign("alice", -time.Second)
	if err != nil {
		fatal(err)
	}
	_, err = Parse(expired)
	fmt.Printf("  ① 过期令牌   → %v\n", err)
	tampered, err := TamperPayload(token, "mallory")
	if err != nil {
		fatal(err)
	}
	_, err = Parse(tampered)
	fmt.Printf("  ② 篡改 sub=mallory → %v\n", err)
	none := CraftAlgNone("mallory")
	_, err = Parse(none)
	fmt.Printf("  ③ alg=none   → %v（WithValidMethods 白名单 + 库默认双保险）\n", err)

	fmt.Println("== 第 3 步：刷新流程——续命容易，吊销无门 ==")
	fresh, err := Refresh(token, 10*time.Minute)
	if err != nil {
		fatal(err)
	}
	if user := mustUser(fresh); user != "alice" {
		fatal(fmt.Errorf("刷新后 user=%s", user))
	}
	if u := mustUser(token); u == "alice" { // 旧令牌在 exp 前依然有效
		fmt.Println("  刷新成功换发 10 分钟新令牌；旧令牌此刻仍能解析——无状态没有吊销")
	}
}

// demoHTTPFlow：第 4 步——Bearer 走真服务。
func demoHTTPFlow() {
	fmt.Println("== 第 4 步：Bearer 走真服务 ==")
	ln, err := net.Listen("tcp", listenOn)
	if err != nil {
		fatal(err)
	}
	srv := &http.Server{Handler: newAPI(), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	waitHealthy()

	client := &http.Client{Timeout: 2 * time.Second}
	get := func(path, bearer string) (int, string) {
		req, _ := http.NewRequest("GET", "http://"+listenOn+path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := client.Do(req)
		if err != nil {
			fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}

	// 登录换令牌（凭证校验是占位：alice/Passw0rd1）
	resp, err := client.Post("http://"+listenOn+"/api/v1/auth/token",
		"application/json", strings.NewReader(`{"username":"alice","password":"Passw0rd1"}`))
	if err != nil {
		fatal(err)
	}
	var out struct {
		Token string `json:"token"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()

	code, body := get("/api/v1/me", out.Token)
	fmt.Printf("  带令牌 /me → %d %s\n", code, body)
	code, body = get("/api/v1/me", "")
	fmt.Printf("  无令牌     → %d %s\n", code, body)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

func mustUser(token string) string {
	u, err := Parse(token)
	if err != nil {
		fatal(err)
	}
	return u
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
