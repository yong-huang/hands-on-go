// Mini Redis —— 基于 TCP 的轻量 KV 缓存服务器
// 面试高频题: TCP 服务器、协议解析、TTL 过期、读写锁并发安全
//
// 支持命令: SET key value, GET key, DEL key, EXPIRE key seconds, TTL key
// 内联 RESP-like 文本协议, 简单行读取 + 空格分词

package main

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// ── 1. 核心数据结构 ──

// KVStore 线程安全的键值存储, 支持 TTL 过期
type KVStore struct {
	mu      sync.RWMutex
	data    map[string]string      // 键值数据
	expiry  map[string]time.Time   // 键的过期时间
	stopCh  chan struct{}           // 通知清理协程退出
}

// NewKVStore 创建一个新的 KVStore 并启动后台过期清理
func NewKVStore() *KVStore {
	store := &KVStore{
		data:   make(map[string]string),
		expiry: make(map[string]time.Time),
		stopCh: make(chan struct{}),
	}
	go store.cleanupLoop()
	return store
}

// Close 停止后台清理协程
func (s *KVStore) Close() {
	close(s.stopCh)
}

// cleanupLoop 后台协程, 每秒扫描并删除过期键
func (s *KVStore) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.removeExpired()
		case <-s.stopCh:
			return
		}
	}
}

// removeExpired 删除所有已过期的键
func (s *KVStore) removeExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, exp := range s.expiry {
		if now.After(exp) {
			delete(s.data, key)
			delete(s.expiry, key)
		}
	}
}

// isExpired 检查键是否已过期 (需持有读锁或写锁)
func (s *KVStore) isExpired(key string) bool {
	if exp, ok := s.expiry[key]; ok {
		return time.Now().After(exp)
	}
	return false
}

// ── 2. 命令实现 ──

// Set 设置键值对
func (s *KVStore) Set(key, value string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	// 设置键值时清除过期时间 (SET 重置 TTL)
	delete(s.expiry, key)
	return "OK"
}

// Get 获取键值, 已过期返回 nil
func (s *KVStore) Get(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.isExpired(key) {
		return ""
	}
	if val, ok := s.data[key]; ok {
		return val
	}
	return ""
}

// Del 删除键
func (s *KVStore) Del(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, existed := s.data[key]
	delete(s.data, key)
	delete(s.expiry, key)
	if existed {
		return 1
	}
	return 0
}

// Expire 设置键的过期时间 (秒)
func (s *KVStore) Expire(key string, seconds int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; !ok {
		return 0
	}
	s.expiry[key] = time.Now().Add(time.Duration(seconds) * time.Second)
	return 1
}

// TTL 获取键的剩余生存时间 (秒)
func (s *KVStore) TTL(key string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.data[key]; !ok {
		return -2 // 键不存在
	}
	exp, ok := s.expiry[key]
	if !ok {
		return -1 // 键存在但无过期时间
	}
	if time.Now().After(exp) {
		return -2 // 已过期视为不存在
	}
	return int(time.Until(exp).Seconds())
}

// ── 3. 协议解析与连接处理 ──

// handleConn 处理单个客户端连接
func handleConn(conn net.Conn, store *KVStore) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		// 读取一行命令 (以 \n 或 \r\n 结尾)
		line, err := reader.ReadString('\n')
		if err != nil {
			return // 客户端断开或读错误
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}

		// 空格分词解析命令
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}

		resp := executeCommand(parts, store)
		fmt.Fprintf(conn, "%s\r\n", resp)
	}
}

// executeCommand 根据命令名分发执行
func executeCommand(parts []string, store *KVStore) string {
	cmd := strings.ToUpper(parts[0])

	switch cmd {
	case "SET":
		if len(parts) != 3 {
			return "ERR wrong number of arguments for 'SET' command"
		}
		return store.Set(parts[1], parts[2])

	case "GET":
		if len(parts) != 2 {
			return "ERR wrong number of arguments for 'GET' command"
		}
		val := store.Get(parts[1])
		if val == "" {
			// 区分 "键不存在" 和 "值为空字符串"
			store.mu.RLock()
			_, exists := store.data[parts[1]]
			expired := store.isExpired(parts[1])
			store.mu.RUnlock()
			if !exists || expired {
				return "(nil)"
			}
		}
		return fmt.Sprintf("\"%s\"", val)

	case "DEL":
		if len(parts) != 2 {
			return "ERR wrong number of arguments for 'DEL' command"
		}
		count := store.Del(parts[1])
		return fmt.Sprintf("(integer) %d", count)

	case "EXPIRE":
		if len(parts) != 3 {
			return "ERR wrong number of arguments for 'EXPIRE' command"
		}
		var seconds int
		if _, err := fmt.Sscanf(parts[2], "%d", &seconds); err != nil {
			return "ERR value is not an integer or out of range"
		}
		count := store.Expire(parts[1], seconds)
		return fmt.Sprintf("(integer) %d", count)

	case "TTL":
		if len(parts) != 2 {
			return "ERR wrong number of arguments for 'TTL' command"
		}
		ttl := store.TTL(parts[1])
		return fmt.Sprintf("(integer) %d", ttl)

	case "PING":
		return "PONG"

	default:
		return fmt.Sprintf("ERR unknown command '%s'", parts[0])
	}
}

// ── 4. 服务器启动 ──

// Start 启动 TCP 服务器, 监听指定地址
func Start(addr string, store *KVStore) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen failed: %w", err)
	}
	defer listener.Close()
	fmt.Printf("Mini Redis server listening on %s\n", listener.Addr())

	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Printf("accept error: %v\n", err)
			continue
		}
		go handleConn(conn, store)
	}
}

// ── 5. 客户端辅助函数 (用于 demo) ──

// sendCommand 向服务器发送一条命令并返回响应
func sendCommand(addr, cmd string) (string, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	fmt.Fprintf(conn, "%s\r\n", cmd)
	reader := bufio.NewReader(conn)
	resp, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(resp, "\r\n"), nil
}

// ── 6. Demo ──

func main() {
	store := NewKVStore()
	defer store.Close()

	// 使用随机可用端口
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		fmt.Printf("failed to find available port: %v\n", err)
		return
	}
	addr := listener.Addr().String()
	listener.Close() // 先关闭, 让 Start 重新监听

	// 在协程中启动服务器
	go func() {
		if err := Start(addr, store); err != nil {
			fmt.Printf("server error: %v\n", err)
		}
	}()

	// 等待服务器启动
	time.Sleep(100 * time.Millisecond)

	fmt.Println("=== Mini Redis Demo ===")
	fmt.Println()

	// SET / GET
	demo(addr, "SET name Alice")
	demo(addr, "SET lang Go")
	demo(addr, "GET name")
	demo(addr, "GET lang")

	fmt.Println()

	// DEL
	demo(addr, "DEL lang")
	demo(addr, "GET lang")

	fmt.Println()

	// EXPIRE / TTL
	demo(addr, "SET session token123")
	demo(addr, "EXPIRE session 10")
	demo(addr, "TTL session")
	demo(addr, "TTL name") // 无过期时间
	demo(addr, "TTL notexist") // 键不存在

	fmt.Println()

	// 等待过期
	fmt.Println("Waiting 2 seconds for TTL to decrease...")
	time.Sleep(2 * time.Second)
	demo(addr, "TTL session")

	fmt.Println()

	// PING
	demo(addr, "PING")

	// 错误命令
	fmt.Println()
	demo(addr, "SET")          // 参数不足
	demo(addr, "UNKNOWN_CMD")  // 未知命令

	fmt.Println()
	fmt.Println("=== Demo Complete ===")
}

// demo 打印一条命令及其响应
func demo(addr, cmd string) {
	resp, err := sendCommand(addr, cmd)
	if err != nil {
		fmt.Printf("> %s  -> ERROR: %v\n", cmd, err)
		return
	}
	fmt.Printf("> %-30s -> %s\n", cmd, resp)
}
