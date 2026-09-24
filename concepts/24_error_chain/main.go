package main

// 24_error_chain：自定义 error 类型与三层错误链
//
// 一个真实请求往往穿过多层：存储层 → 服务层 → API 层。每层都想加一句自己的
// 上下文，但又不能丢掉底层错误——否则最上层的调用方既拿不到 Code 这类结构化
// 字段，也无法做语义判定。本程序搭一条三层包装链，演示：
//   1. 自定义 error 类型：实现 Error() string 就是 error，还能携带结构化字段
//   2. fmt.Errorf("%w") 逐层包装：消息越来越长，链条却原样保留
//   3. errors.As：沿链定位 *DBErr，取出 Code 做程序化处理
//   4. errors.Is：判定链中埋着的哨兵错误
//   5. errors.Unwrap：手动剥一层，看清"链"的本质是单向指针
//
// 运行：go run main.go

import (
	"errors"
	"fmt"
)

// DBErr 是底层（存储层）的错误类型：除了消息还携带结构化字段 Code。
// 实现 Error() string 后它就是合法的 error。
type DBErr struct {
	Code int
	Msg  string
}

func (e *DBErr) Error() string {
	return fmt.Sprintf("db error %d: %s", e.Code, e.Msg)
}

// ErrNotFound 是语义哨兵：调用方用它做"资源不存在"这类业务判定。
var (
	ErrNotFound  = errors.New("record not found")
	ErrForbidden = errors.New("forbidden")
)

// queryUser 模拟底层：既返回自定义类型，也可能返回哨兵错误。
func queryUser(id int) error {
	if id <= 0 {
		return &DBErr{Code: 1050, Msg: "invalid user id"}
	}
	if id > 100 {
		return fmt.Errorf("query users: id=%d: %w", id, ErrNotFound)
	}
	return nil
}

// service 模拟中间层：加上下文（service: ...），用 %w 保住链条。
func service(id int) error {
	if err := queryUser(id); err != nil {
		return fmt.Errorf("service: %w", err)
	}
	return nil
}

// handler 模拟顶层：再加一层上下文，链变成三层。
func handler(id int) error {
	if err := service(id); err != nil {
		return fmt.Errorf("api: %w", err)
	}
	return nil
}

func main() {
	println("========== 1. 三层包装链：消息逐层加长，链条逐层相扣 ==========")
	err := handler(-1)
	fmt.Println(err) // api: service: db error 1050: invalid user id —— 一条字符串串起三层

	println("\n========== 2. errors.As：沿链定位 *DBErr，提取结构化字段 ==========")
	var dbErr *DBErr
	if errors.As(err, &dbErr) {
		fmt.Printf("命中 *DBErr → Code=%d, Msg=%q（As 会逐层 Unwrap，不在乎包了几层）\n", dbErr.Code, dbErr.Msg)
	} else {
		fmt.Println("链中没有 *DBErr")
	}

	println("\n========== 3. errors.Is：判定链中的语义哨兵 ==========")
	err2 := handler(999)
	fmt.Println("err2 本体:", err2)
	fmt.Println("errors.Is(err2, ErrNotFound):", errors.Is(err2, ErrNotFound), "→ 判定为资源不存在，返回 404")
	fmt.Println("errors.Is(err2, ErrForbidden):", errors.Is(err2, ErrForbidden), "→ 其他语义不误判")

	println("\n========== 4. errors.Unwrap：手动剥一层，链的本质是单向指针 ==========")
	fmt.Println("第 0 层:", err2)
	inner1 := errors.Unwrap(err2)
	fmt.Println("剥 1 层:", inner1)
	inner2 := errors.Unwrap(inner1)
	fmt.Println("剥 2 层:", inner2)
	fmt.Println("再往下剥:", errors.Unwrap(inner2), "→ 露出哨兵本身，下一层 Unwrap 返回 nil，链到头了")

	println("\n========== 5. 完整链条：一层层 %w 拼出来的字符串形式 ==========")
	for cur, i := err2, 0; cur != nil; i++ {
		fmt.Printf("layer %d: %v\n", i, cur)
		cur = errors.Unwrap(cur)
	}
}
