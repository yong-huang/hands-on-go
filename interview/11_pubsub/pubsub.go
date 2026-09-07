// Pub/Sub —— 发布订阅系统
// 面试高频题: 主题管理、订阅者注册、异步消息分发、取消订阅

package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ── 1. 核心结构 ──

type subscription struct {
	id     uint64
	topic  string
	ch     chan interface{}
	closed bool
	mu     sync.Mutex
}

type PubSub struct {
	mu          sync.RWMutex
	subs        map[string][]*subscription
	nextID      uint64
}

func NewPubSub() *PubSub {
	return &PubSub{subs: make(map[string][]*subscription)}
}

// Subscribe 订阅主题，返回接收通道和取消订阅函数
func (ps *PubSub) Subscribe(topic string) (<-chan interface{}, func()) {
	s := &subscription{
		id:    atomic.AddUint64(&ps.nextID, 1),
		topic: topic,
		ch:    make(chan interface{}, 64),
	}
	ps.mu.Lock()
	ps.subs[topic] = append(ps.subs[topic], s)
	ps.mu.Unlock()

	unsub := func() {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		s.closed = true
		close(s.ch)
		s.mu.Unlock()

		ps.mu.Lock()
		list := ps.subs[topic]
		for i, sub := range list {
			if sub.id == s.id {
				ps.subs[topic] = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(ps.subs[topic]) == 0 {
			delete(ps.subs, topic)
		}
		ps.mu.Unlock()
	}
	return s.ch, unsub
}

// Publish 发布消息到指定主题（非阻塞）
func (ps *PubSub) Publish(topic string, msg interface{}) int {
	ps.mu.RLock()
	list := ps.subs[topic]
	// copy to avoid holding lock during send
	copied := make([]*subscription, len(list))
	copy(copied, list)
	ps.mu.RUnlock()

	sent := 0
	for _, s := range copied {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			continue
		}
		select {
		case s.ch <- msg:
			sent++
		default:
			// skip slow consumer
		}
		s.mu.Unlock()
	}
	return sent
}

// Subscribers 返回主题的订阅者数量
func (ps *PubSub) Subscribers(topic string) int {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return len(ps.subs[topic])
}

// ── 2. Demo ──

func runDemo() {
	fmt.Println("==============================================")
	fmt.Println("Pub/Sub -- Demo Mode")
	fmt.Println("==============================================")

	ps := NewPubSub()

	// 1) Subscribe to "news"
	fmt.Println("\n[1] Subscribe to 'news':")
	newsCh, unsubNews := ps.Subscribe("news")
	go func() {
		for msg := range newsCh {
			fmt.Printf("  [news subscriber] got: %v\n", msg)
		}
	}()

	// 2) Subscribe to "alerts"
	fmt.Println("\n[2] Subscribe to 'alerts':")
	alertsCh, unsubAlerts := ps.Subscribe("alerts")
	go func() {
		for msg := range alertsCh {
			fmt.Printf("  [alerts subscriber] got: %v\n", msg)
		}
	}()

	// 3) Multiple subscribers on same topic
	fmt.Println("\n[3] Multiple subscribers on 'news':")
	newsCh2, unsubNews2 := ps.Subscribe("news")
	go func() {
		for msg := range newsCh2 {
			fmt.Printf("  [news subscriber #2] got: %v\n", msg)
		}
	}()

	// 4) Publish
	time.Sleep(50 * time.Millisecond) // let subscribers start
	fmt.Println("\n[4] Publishing messages:")
	fmt.Printf("  subscribers('news'): %d\n", ps.Subscribers("news"))
	fmt.Printf("  subscribers('alerts'): %d\n", ps.Subscribers("alerts"))

	sent := ps.Publish("news", "Breaking: Go 1.24 released!")
	fmt.Printf("  Publish('news', 'Breaking...') -> sent to %d subs\n", sent)

	sent = ps.Publish("alerts", "CPU usage > 90%")
	fmt.Printf("  Publish('alerts', 'CPU > 90%%') -> sent to %d subs\n", sent)

	sent = ps.Publish("news", "Update: Go 1.24.1 patch")
	fmt.Printf("  Publish('news', 'Update...') -> sent to %d subs\n", sent)

	time.Sleep(100 * time.Millisecond) // let messages deliver

	// 5) Unsubscribe
	fmt.Println("\n[5] Unsubscribe 'news subscriber #2':")
	unsubNews2()
	fmt.Printf("  subscribers('news'): %d\n", ps.Subscribers("news"))

	sent = ps.Publish("news", "After unsub - only 1 should receive")
	fmt.Printf("  Publish -> sent to %d subs\n", sent)

	time.Sleep(50 * time.Millisecond)

	// 6) Publish to non-existent topic
	fmt.Println("\n[6] Publish to non-existent topic:")
	sent = ps.Publish("unknown", "hello")
	fmt.Printf("  Publish('unknown', 'hello') -> sent to %d subs\n", sent)

	// Cleanup
	unsubNews()
	unsubAlerts()
	fmt.Println("\n==============================================")
}

func main() {
	runDemo()
}
