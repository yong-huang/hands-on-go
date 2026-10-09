package main

// jobs.go：内存任务队列——有界 channel、worker 池、重试与死信、SSRF 防护。
//
// 生产者-消费者（patterns/08 的任务池 + concurrency/19 的 errgroup 纪律）：
//
//	队列：buffered channel（容量 = MaxQueue），满了 Enqueue 返回错误（背压外显）
//	worker：固定 N 个 goroutine 循环取任务；panic 用 recover 捕获——
//	        崩溃的任务进重试，worker 本身不倒（死了整条流水线就停了）
//	重试：最多 2 次（退避 100ms），仍失败进死信队列（dead letter）——
//	        不丢、不静默、可人工复查
//	SSRF：解析 URL 后拒绝内网/环回地址——用户提交的 URL 是服务器的"眼睛"，
//	        不设防就是让攻击者拿服务器当跳板扫内网
//
// 任务可见性：queue depth / done / dead letter 三个原子计数器。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrQueueFull：队列满（背压信号，调用方应拒绝或降级）。
var ErrQueueFull = errors.New("job queue full")

// ErrSSRFBlocked：目标地址落在内网/环回——SSRF 防线。
var ErrSSRFBlocked = errors.New("url targets private network")

// Job：一次标题抓取任务。
type Job struct {
	URL     string
	BookRef int64 // 落库目标（内存书签表的 id）
	Attempt int   // 已尝试次数
}

// DeadLetter：死信记录（任务 + 失败原因链）。
type DeadLetter struct {
	Job Job
	Err string
	At  time.Time
}

// Pool：worker 池。
type Pool struct {
	queue     chan Job
	dead      []DeadLetter
	deadMu    sync.Mutex
	quit      chan struct{}
	wg        sync.WaitGroup
	fetcher   Fetcher
	onDone    func(ref int64, title string, err error) // 落库回调（store 注入）
	depth     atomic.Int64
	processed atomic.Int64
	maxRetry  int
}

// Fetcher：抓取函数（可注入：演示用本地 fixture，测试可注 panic/慢响应）。
type Fetcher func(ctx context.Context, url string) (string, error)

// NewPool：启动 N 个 worker。queue 容量满即背压。
func NewPool(workers, queueSize, maxRetry int, fetcher Fetcher, onDone func(int64, string, error)) *Pool {
	p := &Pool{
		queue:    make(chan Job, queueSize),
		quit:     make(chan struct{}),
		fetcher:  fetcher,
		onDone:   onDone,
		maxRetry: maxRetry,
	}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
	return p
}

// Enqueue：入队（非阻塞——队列满立即返回背压错误）。
func (p *Pool) Enqueue(j Job) error {
	select {
	case p.queue <- j:
		p.depth.Add(1)
		return nil
	default:
		return ErrQueueFull
	}
}

// worker：取任务 → 带 recover 执行 → 成功落库 / 失败重试 / 重试耗尽进死信。
func (p *Pool) worker(id int) {
	defer p.wg.Done()
	for {
		select {
		case <-p.quit:
			return
		case job := <-p.queue:
			p.runOne(job)
			p.depth.Add(-1)
		}
	}
}

// runOne：单次执行，panic 与错误都走重试路径。
func (p *Pool) runOne(job Job) {
	defer func() {
		if r := recover(); r != nil {
			p.fail(job, fmt.Errorf("panic: %v", r))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	title, err := p.fetcher(ctx, job.URL)
	if err != nil {
		p.fail(job, err)
		return
	}
	p.processed.Add(1)
	if p.onDone != nil {
		p.onDone(job.BookRef, title, nil)
	}
}

// fail：重试或死信。
func (p *Pool) fail(job Job, err error) {
	if job.Attempt < p.maxRetry {
		job.Attempt++
		time.Sleep(100 * time.Millisecond) // 固定退避（指数退避在更大规模才有意义）
		p.runOne(job)
		return
	}
	p.deadMu.Lock()
	p.dead = append(p.dead, DeadLetter{Job: job, Err: err.Error(), At: time.Now()})
	p.deadMu.Unlock()
}

// DeadLetters：死信快照。
func (p *Pool) DeadLetters() []DeadLetter {
	p.deadMu.Lock()
	defer p.deadMu.Unlock()
	return append([]DeadLetter(nil), p.dead...)
}

// Depth：当前排队数。
func (p *Pool) Depth() int64 { return p.depth.Load() }

// Shutdown：停止接收新任务，等在途任务收尾（排空语义在实验 18 完整化）。
func (p *Pool) Shutdown(timeout time.Duration) {
	close(p.quit)
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// ---------- SSRF 防线 ----------

// SafeFetch：SSRF 防护的抓取器——解析域名后逐一检查 IP，拒绝环回/私网/链路
// 本地。allowPrivate 是开发开关（本地 fixture 也在环回上），生产必须为 false。
// DNS Rebinding 的彻底防御要自定义 Dialer 在"连接时"再校验一次，
// 本篇在"连接前"这一层拦截（教学口径，README 有边界讨论）。
func SafeFetch(client *http.Client, allowPrivate bool) Fetcher {
	return func(ctx context.Context, url string) (string, error) {
		if err := checkURL(url, allowPrivate); err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("http %d", resp.StatusCode)
		}
		return parseTitle(resp.Body)
	}
}

// checkURL：scheme 白名单 + 目标 IP 黑名单（环回/私网/链路本地/未指定）。
func checkURL(raw string, allowPrivate bool) error {
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return errors.New("only http/https allowed")
	}
	host := hostOf(raw)
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			if allowPrivate {
				continue
			}
			return ErrSSRFBlocked
		}
	}
	return nil
}

// hostOf：取 URL 的主机名。
func hostOf(raw string) string {
	u := raw
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndex(u, "@"); i >= 0 { // 去掉 userinfo（还可混淆主机）
		u = u[i+1:]
	}
	if h, _, err := net.SplitHostPort(u); err == nil {
		return h
	}
	return u
}

// parseTitle：从 HTML 里取 <title>（大小写不敏感；截断 64KB 防超大页面）。
func parseTitle(r io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 64<<10))
	if err != nil {
		return "", err
	}
	s := strings.ToLower(string(raw))
	open := strings.Index(s, "<title>")
	close2 := strings.Index(s, "</title>")
	if open < 0 || close2 < 0 || close2 < open {
		return "", errors.New("no <title> found")
	}
	return strings.TrimSpace(string(raw[open+7 : close2])), nil
}
