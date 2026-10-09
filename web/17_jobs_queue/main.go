// 实验 17：后台任务队列——书签创建后异步抓取页面标题。
//
//	第 1 步：异步抓取——创建即返回，标题随后落库（3 秒验收线的轮询断言）
//	第 2 步：SSRF——生产配置（拒绝内网/环回）下，三类内网 URL 全部被拒进死信
//	第 3 步：队列可见性——depth/processed/dead 三个数字就是健康面板
//
// dev/prod 开关：SafeFetch 的 allowPrivate 参数即"允许内网目标"，
// 开发环境（本地 fixture 也在环回上）放行，生产默认拒绝——同一代码路径，
// 只差一个配置位。
//
// 运行方式：go run .（服务 127.0.0.1:19625，跑完自动退出）；断言见 go test -race。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const listenOn = "127.0.0.1:19625"

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// bookmarkStore：内存书签表（真实 marklet 在实验 12 的 PG 表上做同样回调）。
type bookmarkStore struct {
	mu   sync.Mutex
	rows map[int64]*row
	next int64
}

type row struct {
	ID    int64
	URL   string
	Title string // 空串 = 抓取尚未完成
}

func newStore() *bookmarkStore { return &bookmarkStore{rows: map[int64]*row{}, next: 1} }

func (s *bookmarkStore) create(url string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.next
	s.next++
	s.rows[id] = &row{ID: id, URL: url}
	return id
}

func (s *bookmarkStore) setTitle(id int64, title string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rows[id]; ok {
		r.Title = title
	}
}

func (s *bookmarkStore) get(id int64) *row {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rows[id]; ok {
		cp := *r // 返回副本：调用方在锁外读不到写入方的后续变更（-race 干净）
		return &cp
	}
	return nil
}

// fixtureServer：本地"外网页面"（开发开关下环回可达）。
type fixtureServer struct {
	url    string
	client *http.Client
	srv    *http.Server
}

func newFixtureServer() *fixtureServer {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "<html><head><title>Marklet Fixture</title></head><body>hi</body></html>")
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	go srv.Serve(ln)
	return &fixtureServer{
		url:    "http://" + ln.Addr().String() + "/page",
		client: &http.Client{Timeout: 2 * time.Second},
		srv:    srv,
	}
}

func (f *fixtureServer) Close() { _ = f.srv.Close() }

func main() {
	fixture := newFixtureServer()
	defer fixture.Close()

	store := newStore()
	onDone := func(ref int64, title string, err error) {
		if err == nil {
			store.setTitle(ref, title)
		}
	}

	// 开发池：放行环回（本地 fixture 可达）；生产池：默认拒绝
	devPool := NewPool(2, 16, 2, SafeFetch(fixture.client, true), onDone)
	defer devPool.Shutdown(3 * time.Second)
	prodPool := NewPool(2, 16, 2, SafeFetch(fixture.client, false), onDone)
	defer prodPool.Shutdown(3 * time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/bookmarks", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ URL string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.URL == "" {
			http.Error(w, "need {url}", http.StatusBadRequest)
			return
		}
		id := store.create(in.URL)
		if err := devPool.Enqueue(Job{URL: in.URL, BookRef: id}); err != nil {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
	})

	ln, err := net.Listen("tcp", listenOn)
	if err != nil {
		fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	waitHealthy()

	client := &http.Client{Timeout: 2 * time.Second}

	fmt.Println("== 第 1 步：异步抓取——创建即返回，标题随后落库 ==")
	resp, err := client.Post("http://"+listenOn+"/api/v1/bookmarks",
		"application/json", strings.NewReader(fmt.Sprintf(`{"url":%q}`, fixture.url)))
	if err != nil {
		fatal(err)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	fmt.Printf("  创建 → 201 id=%d（此刻 title 为空——异步的起点）\n", created.ID)

	start := time.Now()
	var title string
	for time.Since(start) < 3*time.Second {
		if b := store.get(created.ID); b != nil && b.Title != "" {
			title = b.Title
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if title == "" {
		fatal(fmt.Errorf("3 秒内标题未落库"))
	}
	fmt.Printf("  %.0fms 内标题落库：%q\n", time.Since(start).Seconds()*1000, title)

	fmt.Println("== 第 2 步：SSRF——生产配置下三类内网 URL 全部被拒 ==")
	for _, bad := range []string{
		"http://127.0.0.1:19625/secret",           // 环回
		"http://169.254.169.254/latest/meta-data", // 云元数据（SSRF 的头号目标）
		"http://10.0.0.5/internal",                // 私网
	} {
		id := store.create(bad)
		_ = prodPool.Enqueue(Job{URL: bad, BookRef: id})
	}
	time.Sleep(700 * time.Millisecond) // 等 worker 走完 检查→重试→死信
	for _, dl := range prodPool.DeadLetters() {
		fmt.Printf("  %-46s → %v\n", dl.Job.URL, dl.Err)
	}

	fmt.Println("== 第 3 步：队列可见性 ==")
	fmt.Printf("  dev: depth=%d processed=%d | prod: dead=%d\n",
		devPool.Depth(), devPool.processed.Load(), len(prodPool.DeadLetters()))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

func waitHealthy() {
	client := &http.Client{Timeout: time.Second}
	for i := 0; i < 100; i++ {
		if resp, err := client.Get("http://" + listenOn + "/healthz"); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fatal(fmt.Errorf("服务 2s 内未就绪"))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
