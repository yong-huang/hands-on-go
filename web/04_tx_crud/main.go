// 实验 04：事务与隔离实测 + CRUD 全量接线。
//
// 同一个业务（把余额减 1）用三种写法各跑一遍并发，答案完全不同：
//
//	第 1 步：无事务读-改-写——读和写是两条独立语句，业务窗口里的值会过期，
//	        10 goroutine × 10 次减 1 后余额守恒被打破（丢更新，lost update）
//	第 2 步：事务版——同样的逻辑包进 WithTx；_txlock=immediate 让事务
//	        一开就持有写锁，读-改-写整体串行，余额精确归零
//	第 3 步：原子 UPDATE 版——n = n - 1 一条语句搞定，同样守恒且最简单
//	第 4 步：CRUD 冒烟——启动即自动迁移，POST/GET/PUT/DELETE 全链路走通
//
// 运行方式：go run .（服务监听 127.0.0.1:19613，跑完自动退出）；断言见 go test -race。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	workers   = 10                   // 并发 goroutine 数
	perWorker = 10                   // 每个执行多少次减 1
	delay     = 2 * time.Millisecond // 业务处理窗口（放大交错概率）
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func main() {
	dir, err := os.MkdirTemp("", "marklet04-*")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx := context.Background()

	store, err := OpenStore(dir + "/marklet.db")
	if err != nil {
		fatal(err)
	}
	defer store.Close()
	if _, err := store.Migrate(); err != nil {
		fatal(err)
	}

	fmt.Println("== 第 1 步：无事务读-改-写——丢更新复现 ==")
	if _, err := store.seedBalance(ctx, workers*perWorker); err != nil {
		fatal(err)
	}
	runParallel(func() error { return store.decrementRacy(ctx, delay) })
	n, err := store.readBalance(ctx)
	if err != nil {
		fatal(err)
	}
	landed := workers*perWorker - n // 实际生效的减法次数
	fmt.Printf("  期望余额 0，实测 %d——%d 次减法只生效 %d 次，丢了 %d 次\n",
		n, workers*perWorker, landed, workers*perWorker-landed)

	fmt.Println("== 第 2 步：事务版（BEGIN IMMEDIATE + WithTx）——守恒 ==")
	if _, err := store.db.Exec(`UPDATE counters SET n = ? WHERE id = 1`, workers*perWorker); err != nil {
		fatal(err)
	}
	runParallel(func() error { return store.decrementTx(ctx, delay) })
	n, err = store.readBalance(ctx)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("  期望余额 0，实测 %d——读-改-写被串行化，一次都没丢\n", n)

	fmt.Println("== 第 3 步：原子 UPDATE 版（n = n - 1）——守恒且最简 ==")
	if _, err := store.db.Exec(`UPDATE counters SET n = ? WHERE id = 1`, workers*perWorker); err != nil {
		fatal(err)
	}
	runParallel(func() error { return store.decrementAtomic(ctx) })
	n, err = store.readBalance(ctx)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("  期望余额 0，实测 %d——自减在一条语句里，没有窗口可过期\n", n)

	fmt.Println("== 第 4 步：CRUD 冒烟——启动即迁移，全链路走通 ==")
	smokeCRUD(dir)
}

// runParallel：workers 个 goroutine 各执行 perWorker 次 op。
func runParallel(op func() error) {
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if err := op(); err != nil {
					fmt.Fprintln(os.Stderr, "操作失败:", err)
				}
			}
		}()
	}
	wg.Wait()
}

// smokeCRUD：用全新库文件走完整启动序列——迁移先于监听端口，
// 然后按真实端口走一遍增查改删。
func smokeCRUD(dir string) {
	apiStore, err := OpenStore(filepath.Join(dir, "api.db"))
	if err != nil {
		fatal(err)
	}
	defer apiStore.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:19613")
	if err != nil {
		fatal(err)
	}
	srv := startServer(apiStore, ln)
	waitHealthy(ln.Addr().String())

	base := "http://" + ln.Addr().String()
	code, body := call("POST", base+"/api/v1/bookmarks", `{"url":"https://go.dev","title":"Go 官网"}`)
	fmt.Printf("  POST   → %d %s\n", code, strings.TrimSpace(body))
	var created struct {
		ID int64 `json:"id"`
	}
	jsonUnmarshal(body, &created)

	_, body = call("GET", fmt.Sprintf("%s/api/v1/bookmarks/%d", base, created.ID), "")
	fmt.Printf("  GET    → %s\n", strings.TrimSpace(body))

	code, _ = call("PUT", fmt.Sprintf("%s/api/v1/bookmarks/%d", base, created.ID),
		`{"title":"Go 官网（改）"}`)
	fmt.Printf("  PUT    → %d\n", code)

	_, body = call("GET", fmt.Sprintf("%s/api/v1/bookmarks/%d", base, created.ID), "")
	fmt.Printf("  GET    → %s\n", strings.TrimSpace(body))

	code, _ = call("DELETE", fmt.Sprintf("%s/api/v1/bookmarks/%d", base, created.ID), "")
	fmt.Printf("  DELETE → %d\n", code)

	code, _ = call("GET", fmt.Sprintf("%s/api/v1/bookmarks/%d", base, created.ID), "")
	fmt.Printf("  GET    → %d（删后再查，404）\n", code)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		fatal(err)
	}
	fmt.Println("  服务已优雅退出")
}

// startServer：marklet 的启动序列——迁移（schema 就绪）先于监听端口，
// 第一个请求进来时表一定已经在。"迁移在服务启动时自动执行"就是这次序。
func startServer(s *Store, ln net.Listener) *http.Server {
	applied, err := s.Migrate()
	if err != nil {
		fatal(err)
	}
	fmt.Printf("  启动迁移：应用 %d 个版本 %v\n", len(applied), applied)
	srv := &http.Server{Handler: newAPI(s), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	return srv
}

// call：冒烟用的极简 HTTP 客户端（每请求新建连接，避免 keep-alive 干扰退出）。
func call(method, url, body string) (int, string) {
	req, err := http.NewRequest(method, url, bytes.NewBufferString(body))
	if err != nil {
		fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// waitHealthy：轮询 /healthz 直到服务可用。
func waitHealthy(addr string) {
	for i := 0; i < 100; i++ {
		if code, _ := call("GET", "http://"+addr+"/healthz", ""); code == http.StatusOK {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fatal(fmt.Errorf("服务 2s 内未就绪"))
}

func jsonUnmarshal(data string, v any) {
	if err := json.Unmarshal([]byte(data), v); err != nil {
		fatal(err)
	}
}
