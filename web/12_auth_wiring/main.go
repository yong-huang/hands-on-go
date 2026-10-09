// 实验 12：全站认证打通——身份从令牌贯穿到每一行数据。
//
// 路由分两组：公开组（注册/登录/healthz）与受保护组（书签 CRUD，Bearer 必过）。
// 数据侧每条查询都带 owner 过滤——别人的书签 = 不存在（404 而非 403，
// 不泄露"这个 id 存在"的事实，IDOR 防御的最后一环）。
//
// 演示流程（两个用户 + 一个攻击视角）：
//
//	第 1 步：注册 alice 与 mallory，各自登录换令牌
//	第 2 步：alice 建书签；未登录访问 → 401；伪造令牌 → 401
//	第 3 步：IDOR——mallory 对 alice 的书签 GET/PUT/DELETE，全部 404
//	第 4 步：alice 自己的正常读写全部 200；列表只含自己的书签
//
// 前置：PG 容器（../05_pg_migration/pg.sh start）。运行：go run .；断言见 go test -race。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	pgDBName = "marklet12"
	listenOn = "127.0.0.1:19621"
)

var jwtSecret = []byte("marklet-demo-secret-key-0123456789abcdef")

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func main() {
	if err := EnsurePGDatabase(pgDBName); err != nil {
		fatal(err)
	}
	db, err := OpenDB(withDB(pgDBName))
	if err != nil {
		fatal(err)
	}
	defer db.Close()
	if err := EnsureSchema(context.Background(), db); err != nil {
		fatal(err)
	}

	ln, err := net.Listen("tcp", listenOn)
	if err != nil {
		fatal(err)
	}
	srv := &http.Server{Handler: newWiredAPI(db), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	waitHealthy()
	demo()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

// newWiredAPI：公开组 + 受保护组的完整路由。
func newWiredAPI(db *sql.DB) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})

	// 公开组：注册 / 登录
	mux.HandleFunc("POST /api/v1/auth/register", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Username == "" {
			writeErr(w, http.StatusBadRequest, "need json {username, password}")
			return
		}
		if err := CreateUser(r.Context(), db, in.Username, in.Password); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
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
		id, ok, err := VerifyUser(r.Context(), db, in.Username, in.Password)
		if err != nil || !ok {
			writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
			return
		}
		claims := jwt.RegisteredClaims{
			Subject:   fmt.Sprint(id),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(jwtSecret)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"token": token, "token_type": "Bearer"})
	})

	// 受保护组：书签 CRUD（owner 从令牌来，不从请求参数来）
	mux.HandleFunc("GET /api/v1/bookmarks", func(w http.ResponseWriter, r *http.Request) {
		all, err := ListBookmarks(r.Context(), db, mustUserID(r))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if all == nil {
			all = []Bookmark{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"bookmarks": all})
	})

	mux.HandleFunc("POST /api/v1/bookmarks", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ URL, Title string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.URL == "" {
			writeErr(w, http.StatusBadRequest, "need json {url, title}")
			return
		}
		id, err := CreateBookmark(r.Context(), db, mustUserID(r), in.URL, in.Title)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
	})

	mux.HandleFunc("GET /api/v1/bookmarks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r)
		if !ok {
			return
		}
		b, err := GetBookmark(r.Context(), db, mustUserID(r), id)
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "no such bookmark") // 与"真不存在"同响应
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
		ok, err := UpdateBookmark(r.Context(), db, mustUserID(r), id, in.Title)
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
		ok, err := DeleteBookmark(r.Context(), db, mustUserID(r), id)
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

	// 中间件：Bearer 只豁免公开组（实验 11 的组装纪律）
	return use(mux, Bearer)
}

// Bearer：受保护组认证（实验 11 同款，豁免规则收窄为三个公开端点）。
func Bearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/api/v1/auth/register", "/api/v1/auth/login":
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="marklet"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		claims := jwt.RegisteredClaims{}
		_, err := jwt.ParseWithClaims(strings.TrimPrefix(auth, "Bearer "), &claims,
			func(*jwt.Token) (any, error) { return jwtSecret, nil },
			jwt.WithValidMethods([]string{"HS256"}))
		if err != nil || claims.Subject == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		uid, err := strconv.ParseInt(claims.Subject, 10, 64)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(withUID(r, uid)))
	})
}

// ---------- 演示 ----------

func demo() {
	base := "http://" + listenOn
	client := &http.Client{Timeout: 3 * time.Second}
	type jar map[string]string

	call := func(j jar, method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		if j["token"] != "" {
			req.Header.Set("Authorization", "Bearer "+j["token"])
		}
		resp, err := client.Do(req)
		if err != nil {
			fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}
	login := func(j jar, user string) {
		code, body := call(j, "POST", "/api/v1/auth/login",
			fmt.Sprintf(`{"username":%q,"password":"Passw0rd1"}`, user))
		if code != http.StatusOK {
			fatal(fmt.Errorf("%s 登录 = %d %s", user, code, body))
		}
		var out struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal([]byte(body), &out)
		j["token"] = out.Token
	}

	alice, mallory := jar{}, jar{}
	fmt.Println("== 第 1 步：注册与登录——两个用户各自持令牌 ==")
	call(alice, "POST", "/api/v1/auth/register", `{"username":"alice","password":"Passw0rd1"}`)
	call(mallory, "POST", "/api/v1/auth/register", `{"username":"mallory","password":"Passw0rd1"}`)
	login(alice, "alice")
	login(mallory, "mallory")
	fmt.Println("  alice 与 mallory 各自持有 HS256 令牌")

	fmt.Println("== 第 2 步：alice 建书签；未登录与伪造令牌全部 401 ==")
	code, body := call(alice, "POST", "/api/v1/bookmarks", `{"url":"https://go.dev","title":"Alice 的秘密书签"}`)
	fmt.Printf("  alice 建   → %d %s\n", code, body)
	code, body = call(jar{}, "GET", "/api/v1/bookmarks", "")
	fmt.Printf("  未登录     → %d %s\n", code, body)
	forged := jar{"token": mustForged()}
	code, body = call(forged, "GET", "/api/v1/bookmarks", "")
	fmt.Printf("  伪造令牌   → %d %s\n", code, body)

	fmt.Println("== 第 3 步：IDOR——mallory 逐个试探 alice 的书签 ==")
	_, body = call(alice, "GET", "/api/v1/bookmarks", "")
	var list struct {
		Bookmarks []struct {
			ID int64 `json:"id"`
		} `json:"bookmarks"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list.Bookmarks) == 0 {
		fatal(fmt.Errorf("alice 列表为空: %s", body))
	}
	bookURL := fmt.Sprintf("/api/v1/bookmarks/%d", list.Bookmarks[0].ID)
	code, _ = call(mallory, "GET", bookURL, "")
	fmt.Printf("  mallory GET    → %d（404 而非 403——不泄露存在性）\n", code)
	code, _ = call(mallory, "PUT", bookURL, `{"title":"hacked"}`)
	fmt.Printf("  mallory PUT    → %d\n", code)
	code, _ = call(mallory, "DELETE", bookURL, "")
	fmt.Printf("  mallory DELETE → %d\n", code)

	fmt.Println("== 第 4 步：alice 的正常读写与列表隔离 ==")
	code, body = call(alice, "GET", bookURL, "")
	fmt.Printf("  alice 自己读   → %d %s\n", code, body)
	code, body = call(alice, "PUT", bookURL, `{"title":"改个名"}`)
	fmt.Printf("  alice 改       → %d\n", code)
	code, body = call(alice, "GET", "/api/v1/bookmarks", "")
	fmt.Printf("  alice 列表     → %d %s（只含自己的）\n", code, body)
}

func mustForged() string {
	claims := jwt.RegisteredClaims{Subject: "999"}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).
		SignedString([]byte("attacker-key-0123456789abcdef0123456789"))
	if err != nil {
		fatal(err)
	}
	return tok
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
