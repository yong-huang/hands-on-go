package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// goleak 门禁：worker 泄漏/任务 goroutine 悬挂在这里现形。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestAsyncPersistWithin3s：验收①——入队后 3 秒内标题落库（异步断言）。
func TestAsyncPersistWithin3s(t *testing.T) {
	store := newStore()
	var onDoneCalled int
	doneMu := sync.Mutex{}
	pool := NewPool(2, 16, 2,
		func(ctx context.Context, url string) (string, error) {
			return "抓到的标题", nil
		},
		func(ref int64, title string, err error) {
			doneMu.Lock()
			onDoneCalled++
			doneMu.Unlock()
			if err == nil {
				store.setTitle(ref, title)
			}
		})
	defer pool.Shutdown(3 * time.Second)

	id := store.create("https://example.com/page")
	if err := pool.Enqueue(Job{URL: "https://example.com/page", BookRef: id}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b := store.get(id); b != nil && b.Title == "抓到的标题" {
			doneMu.Lock()
			n := onDoneCalled
			doneMu.Unlock()
			if n != 1 {
				t.Errorf("onDone 调用 %d 次， want 1", n)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("3 秒内标题未落库")
}

// TestPanicDoesNotLoseJob：验收②——worker 内 panic 被 recover 捕获，
// 任务进重试/死信而不丢，worker 池继续工作（后续任务正常处理）。
func TestPanicDoesNotLoseJob(t *testing.T) {
	store := newStore()
	calls := 0
	var mu sync.Mutex
	pool := NewPool(1, 16, 1, // 单 worker + 重试 1 次：panic 两次后进死信
		func(ctx context.Context, url string) (string, error) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if strings.Contains(url, "poison") {
				panic(fmt.Sprintf("boom #%d", n))
			}
			return "ok 标题", nil
		},
		func(ref int64, title string, err error) {
			if err == nil {
				store.setTitle(ref, title)
			}
		})
	defer pool.Shutdown(3 * time.Second)

	// 先投毒药任务，再投正常任务——正常任务必须仍被处理（worker 没死）
	_ = pool.Enqueue(Job{URL: "https://poison.example/1", BookRef: store.create("poison")})
	id := store.create("https://good.example/1")
	_ = pool.Enqueue(Job{URL: "https://good.example/1", BookRef: id})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b := store.get(id); b != nil && b.Title != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b := store.get(id); b == nil || b.Title == "" {
		t.Fatal("poison 之后正常任务未被处理——worker 死亡")
	}
	dl := pool.DeadLetters()
	if len(dl) != 1 || !strings.Contains(dl[0].Err, "panic") {
		t.Errorf("死信 = %+v， want 1 条 panic 记录", dl)
	}
	if calls < 2 {
		t.Errorf("poison 任务被尝试 %d 次， want ≥2（重试发生过）", calls)
	}
}

// TestSSRFRejected：验收③——环回/私网 URL 在生产配置下被拒，
// 重试耗尽进死信，任务不会真的发起请求。
func TestSSRFRejected(t *testing.T) {
	attempts := 0
	var mu sync.Mutex
	fetcher := SafeFetch(&http.Client{Timeout: time.Second}, false) // 生产配置

	// 环回地址：fixture 自己就是环回——用它验证"被拒"最直接
	fixture := newFixtureServer()
	defer fixture.Close()

	pool := NewPool(1, 16, 1, func(ctx context.Context, url string) (string, error) {
		mu.Lock()
		attempts++
		mu.Unlock()
		return fetcher(ctx, url)
	}, nil)
	defer pool.Shutdown(3 * time.Second)

	_ = pool.Enqueue(Job{URL: fixture.url, BookRef: 0}) // 环回 → SSRF 拒绝
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(pool.DeadLetters()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	dl := pool.DeadLetters()
	if len(dl) != 1 {
		t.Fatalf("死信 = %d 条， want 1", len(dl))
	}
	if !strings.Contains(dl[0].Err, "private network") {
		t.Errorf("死信原因 = %q， want SSRF 拒绝", dl[0].Err)
	}
	// 尝试次数 = 2（初次 + 1 重试），且每次都在"连接前"被拦——没有真实请求副作用
	mu.Lock()
	n := attempts
	mu.Unlock()
	if n != 2 {
		t.Errorf("尝试次数 = %d， want 2", n)
	}
}

// TestQueueFullBackpressure：队列满时 Enqueue 立即返回背压错误。
func TestQueueFullBackpressure(t *testing.T) {
	release := make(chan struct{})
	blocker := make(chan struct{})
	pool := NewPool(1, 1, 0, func(ctx context.Context, url string) (string, error) {
		<-release // 卡住唯一 worker
		return "", errors.New("stopped")
	}, nil)
	defer func() { close(release); pool.Shutdown(time.Second) }()
	close(blocker)

	// 队列容量 1：等 worker 取走 "a"（depth 回落），队列空但 worker 被卡
	_ = pool.Enqueue(Job{URL: "a"})
	deadline := time.Now().Add(time.Second)
	for pool.Depth() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := pool.Enqueue(Job{URL: "b"}); err != nil {
		t.Fatalf("第 2 个入队不应背压: %v", err)
	}
	if err := pool.Enqueue(Job{URL: "c"}); !errors.Is(err, ErrQueueFull) {
		t.Errorf("第 3 个入队 err = %v， want ErrQueueFull", err)
	}
}

// TestURLGuardUnit：checkURL 的单元面——scheme 白名单与私网判定。
func TestURLGuardUnit(t *testing.T) {
	prod := false
	if err := checkURL("ftp://example.com", prod); err == nil {
		t.Error("ftp 应被 scheme 白名单拒绝")
	}
	if err := checkURL("http://127.0.0.1/x", prod); !errors.Is(err, ErrSSRFBlocked) {
		t.Errorf("环回 err = %v， want ErrSSRFBlocked", err)
	}
	if err := checkURL("http://127.0.0.1/x", true); err != nil {
		t.Errorf("开发开关下环回应放行: %v", err)
	}
}
