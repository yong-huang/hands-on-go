package main

// 22_context：Context 的取消传播
//
// Context 是 Go 里跨 API/goroutine 传递"取消信号 + 截止时间 + 请求级数据"的标准方式。
// 本程序演示四件事：
//   1. WithCancel：父 ctx 手动 cancel，子 goroutine 从 ctx.Done() 收到信号后退出
//   2. WithTimeout：任务需要 3 秒，但 ctx 只给 1.5 秒 → 被打断，ctx.Err() 是 DeadlineExceeded
//   3. WithValue：旁路携带请求级数据（如 user），子层直接取出
//   4. 树形传播：父取消后，子/孙所有 goroutine 的 Done 同时关闭，WaitGroup 对账全部退出
//
// 运行：go run main.go（约 2~3 秒）

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type ctxKey string

func main() {
	// ========== 1. WithCancel：手动取消 ==========
	fmt.Println("========== 1. WithCancel：父取消，子收到 Done 退出 ==========")
	ctx1, cancel1 := context.WithCancel(context.Background())
	var wg1 sync.WaitGroup
	wg1.Add(1)
	go func() {
		defer wg1.Done()
		select {
		case <-time.After(10 * time.Second): // 假装一个长任务
			fmt.Println("  子: 任务正常完成（不该发生）")
		case <-ctx1.Done():
			fmt.Printf("  子: 收到取消信号 %v，提前退出\n", ctx1.Err())
		}
	}()
	time.Sleep(2 * time.Second) // 让子任务跑一会儿
	cancel1()                   // 父调用 cancel：向子 goroutine 广播取消
	wg1.Wait()
	fmt.Println("  父: 子已退出，WaitGroup 对账完毕")

	// ========== 2. WithTimeout：超时自动取消 ==========
	fmt.Println("\n========== 2. WithTimeout：1.5s 截止，3s 的任务被打断 ==========")
	ctx2, cancel2 := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel2() // 惯例：无论路径如何都释放资源
	start := time.Now()
	var wg2 sync.WaitGroup
	wg2.Add(1)
	go func() {
		defer wg2.Done()
		select {
		case <-time.After(3 * time.Second): // 任务本身要 3 秒
			fmt.Println("  任务: 跑完了（不该发生）")
		case <-ctx2.Done():
			fmt.Printf("  任务: 被超时打断，耗时 %dms，ctx.Err() = %v\n",
				time.Since(start).Milliseconds(), ctx2.Err())
		}
	}()
	wg2.Wait()

	// ========== 3. WithValue：旁路携带请求级数据 ==========
	fmt.Println("\n========== 3. WithValue：塞 user 值，子层取出 ==========")
	ctx3 := context.WithValue(context.Background(), ctxKey("user"), "alice")
	handleRequest(ctx3)

	// ========== 4. 树形传播：父取消，子+孙一起退出 ==========
	fmt.Println("\n========== 4. 树形传播：一次 cancel，三代全退 ==========")
	base, rootCancel := context.WithCancel(context.Background())
	root := context.WithValue(base, ctxKey("user"), "alice") // 派生 ctx：取消语义不变，额外带上值
	var wg sync.WaitGroup
	wg.Add(2)
	go child(root, &wg)                // 子 goroutine：直接用根 ctx
	go grandchild(root, &wg)           // 孙 goroutine：与子共享同一个 Done
	time.Sleep(300 * time.Millisecond) // 让两代都跑起来
	fmt.Println("  根: 调用 cancel()，广播给整棵树")
	rootCancel()
	wg.Wait() // 对账：子+孙都必须已退出，否则 Wait 永远阻塞
	fmt.Println("  根: WaitGroup 归零，子与孙全部退出")

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. 取消是广播：cancel() 后所有派生 ctx 的 Done channel 同时关闭")
	fmt.Println("2. WithTimeout/WithDeadline 到点自动 cancel，Err() 返回 DeadlineExceeded")
	fmt.Println("3. select ctx.Done() 是 goroutine 响应取消的标准姿势")
	fmt.Println("4. WithValue 只放请求级元数据，键用自定义类型避免冲突")
}

// child 子 goroutine：监听 ctx.Done()，等待期间顺便看一眼 value
func child(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	u, _ := ctx.Value(ctxKey("user")).(string)
	fmt.Printf("  子: 携带的 user = %q，等待取消...\n", u)
	<-ctx.Done()
	fmt.Printf("  子: Done 关闭（%v），退出\n", ctx.Err())
}

// grandchild 孙 goroutine：与子共享根 ctx 的 Done，树形传播的意义所在
func grandchild(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	fmt.Println("  孙: 等待取消...")
	<-ctx.Done()
	fmt.Printf("  孙: Done 关闭（%v），退出\n", ctx.Err())
}

// handleRequest 模拟一层服务：从 ctx 里取出请求级数据
func handleRequest(ctx context.Context) {
	user, ok := ctx.Value(ctxKey("user")).(string) // 类型断言取值，ok 判缺失
	if !ok {
		fmt.Println("  handler: 未登录")
		return
	}
	fmt.Printf("  handler: 为用户 %q 服务（值经 ctx 旁路传入，不走参数）\n", user)
}
