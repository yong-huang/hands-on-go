// 项目 18 验收测试：
// ① 5 层 cancel 树取消根，全部叶子 < 10ms 内 Done；
// ② 父超时自动取消子（原因沿树继承）；
// ③ 子先取消不扩散到父（隔离）。
// 全部用例在 -race 下运行；与标准库的差异表见 README。
package main

import (
	"sync"
	"testing"
	"time"
)

// TestFiveLevelTreeCancelUnder10ms：链式 5 层 + 每层 2 叶 = 10 叶，
// 全部叶子先在 Done 上停好，cancel 根后整树 Done 必须 < 10ms。
func TestFiveLevelTreeCancelUnder10ms(t *testing.T) {
	root, cancel := WithCancel(Background())
	node := root
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		next, _ := WithCancel(node)
		for j := 0; j < 2; j++ {
			leaf, _ := WithCancel(next)
			wg.Add(1)
			go func() {
				<-leaf.Done()
				wg.Done()
			}()
		}
		node = next
	}
	time.Sleep(5 * time.Millisecond) // 让全部叶子观察者停进 Done
	start := time.Now()
	cancel()
	wg.Wait()
	if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
		t.Fatalf("整树 Done 用时 %v, 验收线 10ms", elapsed)
	}
	if err := node.Err(); err != ErrCanceled {
		t.Fatalf("最深层 Err = %v, want %v", err, ErrCanceled)
	}
}

// TestTimeoutCascade：父带 20ms 超时，子/孙自动取消并继承
// ErrDeadlineExceeded；主动取消（先 Stop 计时器）则是 ErrCanceled。
func TestTimeoutCascade(t *testing.T) {
	parent, cancelP := WithTimeout(Background(), 20*time.Millisecond)
	defer cancelP()
	child, _ := WithCancel(parent)
	grandchild, _ := WithCancel(child)
	select {
	case <-child.Done():
	case <-time.After(time.Second):
		t.Fatal("父超时后子未被取消")
	}
	<-grandchild.Done()
	if err := child.Err(); err != ErrDeadlineExceeded {
		t.Fatalf("子 Err = %v, want %v（原因应沿树继承）", err, ErrDeadlineExceeded)
	}
	if _, ok := parent.Deadline(); !ok {
		t.Fatal("timerCtx 的 Deadline() 应有值")
	}
}

// TestActiveCancelBeatsDeadline：在计时器到点前主动 cancel，
// Err 必须是 ErrCanceled 而不是 ErrDeadlineExceeded。
func TestActiveCancelBeatsDeadline(t *testing.T) {
	ctx, cancel := WithTimeout(Background(), 50*time.Millisecond)
	cancel() // 立刻主动取消
	<-ctx.Done()
	if err := ctx.Err(); err != ErrCanceled {
		t.Fatalf("主动取消后 Err = %v, want %v", err, ErrCanceled)
	}
}

// TestDeadlineShortCircuit：父截止时间更早时退化为 WithCancel，
// 子仍会在父的时点被取消，继承 ErrDeadlineExceeded（标准库同款短路）。
func TestDeadlineShortCircuit(t *testing.T) {
	parent, cancelP := WithDeadline(Background(), time.Now().Add(20*time.Millisecond))
	defer cancelP()
	child, cancelC := WithDeadline(parent, time.Now().Add(time.Minute))
	defer cancelC()
	<-child.Done()
	if err := child.Err(); err != ErrDeadlineExceeded {
		t.Fatalf("子 Err = %v, want %v", err, ErrDeadlineExceeded)
	}
}

// TestChildCancelIsolation：子先取消不扩散——父与兄弟不受影响；
// 随后父取消，兄弟照常级联。
func TestChildCancelIsolation(t *testing.T) {
	father, cancelF := WithCancel(Background())
	defer cancelF()
	son, cancelS := WithCancel(father)
	daughter, _ := WithCancel(father)

	cancelS()
	<-son.Done()
	if err := son.Err(); err != ErrCanceled {
		t.Fatalf("son Err = %v, want %v", err, ErrCanceled)
	}
	select { // 父与兄弟必须纹丝不动
	case <-father.Done():
		t.Fatal("子取消波及了父")
	case <-daughter.Done():
		t.Fatal("子取消波及了兄弟")
	case <-time.After(50 * time.Millisecond):
	}

	cancelF() // 父取消：兄弟正常级联
	<-daughter.Done()
	<-father.Done()
}

// TestCancelIdempotent：重复取消是空操作；Done 只关一次（二次 close
// 会 panic，等价于隐式断言）。
func TestCancelIdempotent(t *testing.T) {
	ctx, cancel := WithCancel(Background())
	cancel()
	cancel()
	cancel()
	<-ctx.Done()
	if err := ctx.Err(); err != ErrCanceled {
		t.Fatalf("Err = %v, want %v", err, ErrCanceled)
	}
}

// TestParentAlreadyCanceled：父已取消后新挂的孩子生来就是取消态，
// 且继承父的错误（propagateCancel 的登记时复查路径）。
func TestParentAlreadyCanceled(t *testing.T) {
	parent, cancel := WithCancel(Background())
	cancel()
	child, _ := WithCancel(parent)
	select {
	case <-child.Done():
	case <-time.After(time.Second):
		t.Fatal("已取消父下新建的子应立即 Done")
	}
	if err := child.Err(); err != ErrCanceled {
		t.Fatalf("子 Err = %v, want %v", err, ErrCanceled)
	}
}

// fakeParent：外部 Context 实现（非本库类型），用于覆盖 watcher 路径。
type fakeParent struct {
	done chan struct{}
	err  error
}

func (f fakeParent) Deadline() (time.Time, bool) { return time.Time{}, false }
func (f fakeParent) Done() <-chan struct{}       { return f.done }
func (f fakeParent) Err() error                  { return f.err }
func (f fakeParent) Value(any) any               { return nil }

// TestExternalParentWatcher：父不是本库类型时起 watcher goroutine 盯
// parent.Done()，父取消后子继承父的错误。
func TestExternalParentWatcher(t *testing.T) {
	done := make(chan struct{})
	parent := fakeParent{done: done, err: ErrCanceled}
	child, cancel := WithCancel(parent)
	defer cancel()
	close(done) // 外部父"取消"
	select {
	case <-child.Done():
	case <-time.After(time.Second):
		t.Fatal("watcher 未把外部父的取消传播给子")
	}
	if err := child.Err(); err != ErrCanceled {
		t.Fatalf("子 Err = %v, want %v", err, ErrCanceled)
	}
}

// TestValueChain：就近命中遮蔽父层；跨层回溯；缺失返回 nil；nil 键 panic。
func TestValueChain(t *testing.T) {
	ctx := WithValue(WithValue(WithValue(Background(), "k", 1), "k", 2), "other", 3)
	if v := ctx.Value("k"); v != 2 {
		t.Fatalf("k = %v, want 2（就近命中）", v)
	}
	if v := ctx.Value("other"); v != 3 {
		t.Fatalf("other = %v, want 3（跨层回溯）", v)
	}
	if v := ctx.Value("missing"); v != nil {
		t.Fatalf("missing = %v, want nil", v)
	}
	defer func() {
		if recover() == nil {
			t.Error("nil key 应 panic")
		}
	}()
	WithValue(Background(), nil, 1)
}

// TestCancelTreeConcurrent：并发取消多个子树——先到先得的幂等性保证
// 不重不漏（-race 高压 children 登记表）。
func TestCancelTreeConcurrent(t *testing.T) {
	root, cancelRoot := WithCancel(Background())
	defer cancelRoot()
	leaves := make([]Context, 0, 30)
	for i := 0; i < 10; i++ { // 3 层 × 10 叉
		mid, _ := WithCancel(root)
		for j := 0; j < 3; j++ {
			leaf, cancelLeaf := WithCancel(mid)
			leaves = append(leaves, leaf)
			go cancelLeaf() // 与父取消赛跑
		}
	}
	for _, leaf := range leaves {
		select {
		case <-leaf.Done():
		case <-time.After(time.Second):
			t.Fatal("叶子未被取消")
		}
	}
}
