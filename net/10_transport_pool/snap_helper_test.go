package main

// snap：账本三计数的快照（测试辅助；main 直接读原子量）。
func snap(c *counters) (newConns, closedConns, requests int64) {
	return c.newConns.Load(), c.closedConns.Load(), c.requests.Load()
}
