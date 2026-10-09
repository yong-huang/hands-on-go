package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

// goleak 门禁。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// newObsTest：把根 logger 指向 buffer 的观测台，返回服务与缓冲。
func newObsTest() (http.Handler, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	root := newBufLogger(buf)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/work", func(w http.ResponseWriter, r *http.Request) {
		LoggerFrom(r.Context()).Info("handler doing work", "step", 1)
		w.Write([]byte("done"))
	})
	mux.HandleFunc("POST /api/v1/login", func(w http.ResponseWriter, r *http.Request) {
		LogSafe(LoggerFrom(r.Context()), slogLevelInfo, "login attempt",
			"username", "alice", "password", "SuperSecret123", "token", "tok_abc123")
		w.Write([]byte("ok"))
	})
	h := use(mux, RequestIDWithRoot(root), AccessLog)
	return h, buf
}

// TestRequestIDConsistent：验收①——一次请求中，中间件与 handler 的日志
// request_id 完全一致；响应头回带同值。
func TestRequestIDConsistent(t *testing.T) {
	h, buf := newObsTest()

	req := httptest.NewRequest("GET", "/api/v1/work", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("响应头缺 X-Request-Id")
	}
	id := rec.Header().Get("X-Request-Id")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("日志不足 2 行: %q", buf.String())
	}
	seen := map[string]int{}
	for _, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("日志非合法 JSON: %v", err)
		}
		rid, _ := row["request_id"].(string)
		if rid != id {
			t.Errorf("日志 request_id=%q，响应头=%q", rid, id)
		}
		seen[row["msg"].(string)]++
	}
	if seen["handler doing work"] != 1 || seen["access"] != 1 {
		t.Errorf("日志行缺失: %v", seen)
	}
}

// TestRequestIDPassthrough：验收②——客户端传 X-Request-Id 时原样透传。
func TestRequestIDPassthrough(t *testing.T) {
	h, buf := newObsTest()

	req := httptest.NewRequest("GET", "/api/v1/work", nil)
	req.Header.Set("X-Request-Id", "my-trace-id-001")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-Id"); got != "my-trace-id-001" {
		t.Errorf("响应头 = %q， want 透传值", got)
	}
	if !strings.Contains(buf.String(), `"request_id":"my-trace-id-001"`) {
		t.Errorf("日志未透传客户端 ID: %s", buf.String())
	}
}

// TestRedaction：验收③——日志里 grep 不到密码与 token 明文，只有 ***。
func TestRedaction(t *testing.T) {
	h, buf := newObsTest()

	req := httptest.NewRequest("POST", "/api/v1/login",
		strings.NewReader(`{"username":"alice","password":"SuperSecret123","token":"tok_abc123"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	out := buf.String()
	for _, secret := range []string{"SuperSecret123", "tok_abc123"} {
		if strings.Contains(out, secret) {
			t.Errorf("日志泄露敏感值 %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, `"password":"`+Redact+`"`) || !strings.Contains(out, `"token":"`+Redact+`"`) {
		t.Errorf("脱敏字段缺失（应含 ***）:\n%s", out)
	}
	// 非敏感字段不受影响
	if !strings.Contains(out, `"username":"alice"`) {
		t.Errorf("username 不该被脱敏:\n%s", out)
	}
}

// TestEmptyHeaderRegenerates：空/超长透传头不进入日志——重新生成。
func TestEmptyHeaderRegenerates(t *testing.T) {
	h, buf := newObsTest()
	req := httptest.NewRequest("GET", "/api/v1/work", nil)
	req.Header.Set("X-Request-Id", strings.Repeat("x", 500)) // 超长垃圾
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(buf.String(), strings.Repeat("x", 500)) {
		t.Error("超长透传头被原样写进日志")
	}
}
