package main

import (
	"os"
	"strings"
	"testing"
)

// TestDockerfileContract：交付契约钉在测试里——多阶段、非 root、健康检查、
// 静态编译四要素缺一即失败（改动 Dockerfile 时这是最后一道闸）。
func TestDockerfileContract(t *testing.T) {
	data, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	checks := []struct{ name, substr string }{
		{"多阶段构建", "AS build"},
		{"静态编译", "CGO_ENABLED=0"},
		{"非 root 用户", "USER marklet"},
		{"健康检查", "HEALTHCHECK"},
	}
	for _, c := range checks {
		if !strings.Contains(s, c.substr) {
			t.Errorf("Dockerfile 缺 %s（%q）", c.name, c.substr)
		}
	}
	if strings.Count(s, "FROM") != 2 {
		t.Errorf("FROM 数量 = %d， want 2（构建段 + 运行段）", strings.Count(s, "FROM"))
	}
}

// TestComposeWiring：compose 的接线契约——健康条件、数据卷、宽限期。
func TestComposeWiring(t *testing.T) {
	data, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, substr := range []string{
		"condition: service_healthy", // 等 PG 真就绪
		"pgdata:",                    // 数据卷
		"stop_grace_period",          // 停机宽限（实验 18 的预算）
	} {
		if !strings.Contains(s, substr) {
			t.Errorf("compose 缺 %q", substr)
		}
	}
}
