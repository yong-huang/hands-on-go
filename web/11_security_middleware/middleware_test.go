package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/goleak"
)

// goleak 门禁。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func testServer() http.Handler { return newSecureAPI() }

func goodToken(t *testing.T) string {
	t.Helper()
	claims := jwt.RegisteredClaims{Subject: "alice"}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(jwtSecret))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// fetch：带可选头的请求助手，返回状态码、响应头、CSRF Cookie、响应体。
func fetch(t *testing.T, h http.Handler, method, path, body string, hdr map[string]string) (int, http.Header, string, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var csrf string
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == csrfCookie {
			csrf = ck.Value
		}
	}
	return rec.Code, rec.Header(), csrf, rec.Body.String()
}

// TestBearerAuth：验收①——无令牌 401、伪造令牌 401、有效令牌 200；
// 401 必须带 WWW-Authenticate 头（HTTP 语义）。
func TestBearerAuth(t *testing.T) {
	h := testServer()

	code, hdr, _, _ := fetch(t, h, "GET", "/api/v1/me", "", nil)
	if code != http.StatusUnauthorized {
		t.Errorf("无令牌 = %d， want 401", code)
	}
	if hdr.Get("WWW-Authenticate") == "" {
		t.Error("401 缺 WWW-Authenticate 头")
	}

	code, _, _, _ = fetch(t, h, "GET", "/api/v1/me", "", map[string]string{
		"Authorization": "Bearer forged.token.value",
	})
	if code != http.StatusUnauthorized {
		t.Errorf("伪造令牌 = %d， want 401", code)
	}

	code, _, _, body := fetch(t, h, "GET", "/api/v1/me", "", map[string]string{
		"Authorization": "Bearer " + goodToken(t),
	})
	if code != http.StatusOK || !strings.Contains(body, "alice") {
		t.Errorf("有效令牌 = %d %s， want 200 alice", code, body)
	}
	// 探针与令牌发放点豁免
	if code, _, _, _ = fetch(t, h, "GET", "/healthz", "", nil); code != http.StatusOK {
		t.Errorf("healthz = %d， want 200（豁免失效）", code)
	}
}

// TestCSRFDoubleSubmit：验收②——写请求缺 CSRF 头 403、令牌错误 403、
// 双证齐全 200；幂等方法不校验。
func TestCSRFDoubleSubmit(t *testing.T) {
	h := testServer()
	tok := goodToken(t)
	auth := map[string]string{"Authorization": "Bearer " + tok}

	// 取令牌（单一出口）
	code, hdr, csrf, _ := fetch(t, h, "GET", "/api/v1/csrf", "", nil)
	if code != http.StatusOK || csrf == "" {
		t.Fatalf("csrf 端点 = %d csrf=%q", code, csrf)
	}
	if !strings.Contains(hdr.Get("Set-Cookie"), csrf) {
		t.Fatal("Set-Cookie 与响应体令牌不一致（双提交断裂）")
	}

	withCSRF := map[string]string{
		"Authorization": "Bearer " + tok,
		"Cookie":        csrfCookie + "=" + csrf,
		"X-CSRF-Token":  csrf,
	}
	// 缺头
	code, _, _, _ = fetch(t, h, "POST", "/api/v1/transfer", `{}`, auth)
	if code != http.StatusForbidden {
		t.Errorf("缺 CSRF 头 = %d， want 403", code)
	}
	// 头与 Cookie 不等
	code, _, _, _ = fetch(t, h, "POST", "/api/v1/transfer", `{}`, map[string]string{
		"Authorization": "Bearer " + tok,
		"Cookie":        csrfCookie + "=" + csrf,
		"X-CSRF-Token":  "wrong-token",
	})
	if code != http.StatusForbidden {
		t.Errorf("CSRF 错误 = %d， want 403", code)
	}
	// 双证齐全
	code, _, _, body := fetch(t, h, "POST", "/api/v1/transfer", `{"amount":1}`, withCSRF)
	if code != http.StatusOK || !strings.Contains(body, "alice") {
		t.Errorf("双证齐全 = %d %s， want 200", code, body)
	}
	// GET 不校验（幂等）
	if code, _, _, _ = fetch(t, h, "GET", "/api/v1/me", "", auth); code != http.StatusOK {
		t.Errorf("GET 带 Bearer = %d， want 200", code)
	}
}

// TestSecureHeaders：验收③——五项安全头逐项断言。
func TestSecureHeaders(t *testing.T) {
	h := testServer()
	code, hdr, _, _ := fetch(t, h, "GET", "/healthz", "", nil)
	if code != http.StatusOK {
		t.Fatalf("healthz = %d", code)
	}
	want := map[string]string{
		"Content-Security-Policy":   "default-src 'none'",
		"X-Frame-Options":           "DENY",
		"X-Content-Type-Options":    "nosniff",
		"Strict-Transport-Security": "max-age=63072000; includeSubDomains",
		"Referrer-Policy":           "no-referrer",
	}
	for k, v := range want {
		if hdr.Get(k) != v {
			t.Errorf("%s = %q， want %q", k, hdr.Get(k), v)
		}
	}
}

// TestMaxBody：验收④——恰好 2MB 通过，2MB+1 字节 413。
func TestMaxBody(t *testing.T) {
	h := testServer()
	tok := goodToken(t)
	_, _, csrf, _ := fetch(t, h, "GET", "/api/v1/csrf", "", nil)
	hdr := map[string]string{
		"Authorization": "Bearer " + tok,
		"Cookie":        csrfCookie + "=" + csrf,
		"X-CSRF-Token":  csrf,
	}

	code, _, _, body := fetch(t, h, "POST", "/api/v1/transfer", strings.Repeat("x", 2<<20), hdr)
	if code != http.StatusOK {
		t.Errorf("恰好 2MB = %d %s， want 200", code, body)
	}
	code, _, _, _ = fetch(t, h, "POST", "/api/v1/transfer", strings.Repeat("x", 2<<20)+"x", hdr)
	if code != http.StatusRequestEntityTooLarge {
		t.Errorf("2MB+1 = %d， want 413", code)
	}
}
