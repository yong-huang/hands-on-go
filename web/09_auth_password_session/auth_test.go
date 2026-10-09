package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// goleak 门禁。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestArgon2SaltAndVerify：验收①——同一密码两次哈希不同（盐生效）且都能通过验证；
// 错误密码必须失败。
func TestArgon2SaltAndVerify(t *testing.T) {
	const pw = "correct horse battery staple"
	h1, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("两次哈希相同——盐未生效")
	}
	if ok, err := VerifyPassword(pw, h1); err != nil || !ok {
		t.Errorf("hash1 验证: ok=%v err=%v", ok, err)
	}
	if ok, err := VerifyPassword(pw, h2); err != nil || !ok {
		t.Errorf("hash2 验证: ok=%v err=%v", ok, err)
	}
	if ok, _ := VerifyPassword("wrong", h1); ok {
		t.Error("错误密码通过了验证")
	}
	// PHC 串里必须能找回参数（验证时按存储的参数重算）
	for _, field := range []string{"$argon2id$", "m=32768", "t=2", "p=2"} {
		if !strings.Contains(h1, field) {
			t.Errorf("PHC 串缺 %q: %s", field, h1)
		}
	}
}

// TestWeakPasswordPolicy：验收④——弱密码用例表，逐条断言违规原因。
func TestWeakPasswordPolicy(t *testing.T) {
	cases := []struct {
		pw   string
		want []string // 期望包含的违规原因（空表 = 通过）
	}{
		{"12345678", []string{"未包含字母"}},
		{"abcdefgh", []string{"未包含数字"}},
		{"a1", []string{"长度不足 8 位"}},
		{"short1", []string{"长度不足 8 位"}},
		{"Passw0rd1", nil},
	}
	for _, tc := range cases {
		got := CheckPasswordPolicy(tc.pw)
		if tc.want == nil {
			if len(got) != 0 {
				t.Errorf("%q 应通过，实际拒绝: %v", tc.pw, got)
			}
			continue
		}
		if len(got) == 0 {
			t.Errorf("%q 应被拒绝", tc.pw)
			continue
		}
		for _, reason := range tc.want {
			found := false
			for _, g := range got {
				if g == reason {
					found = true
				}
			}
			if !found {
				t.Errorf("%q 的拒绝原因缺 %q: %v", tc.pw, reason, got)
			}
		}
	}
}

func newTestAuth(t *testing.T) (*SessionStore, http.Handler) {
	t.Helper()
	store, err := NewSessionStore(redisAddr)
	if err != nil {
		t.Skipf("Redis 不可用：%v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, newAuthAPI(store, NewUsers())
}

// callAuth：带 Cookie 罐的请求助手，返回状态码、Set-Cookie、响应体。
func callAuth(t *testing.T, h http.Handler, jar []*http.Cookie, method, path, body string) (int, []*http.Cookie, string) {
	t.Helper()
	req, _ := http.NewRequest(method, "/api/v1/auth"+path, strings.NewReader(body))
	for _, ck := range jar {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	b := rec.Body.String()
	return rec.Code, rec.Result().Cookies(), b
}

// TestSessionRotationOnLogin：验收②——登录轮换：预置旧 SID，
// 登录后新 SID ≠ 旧 SID 且旧 SID 立即作废（会话固定攻击的防御断言）。
func TestSessionRotationOnLogin(t *testing.T) {
	store, h := newTestAuth(t)
	ctx := context.Background()

	// 预置：注册用户 + 攻击者塞给受害者的"已知旧 SID"
	if _, ck, _ := callAuth(t, h, nil, "POST", "/register", `{"username":"bob","password":"Passw0rd1"}`); len(ck) > 0 {
		t.Fatal("注册不应发会话")
	}
	oldSID, err := store.Create(ctx, "anonymous", false)
	if err != nil {
		t.Fatal(err)
	}
	oldCookie := []*http.Cookie{{Name: cookieName, Value: oldSID}}

	code, cookies, _ := callAuth(t, h, oldCookie, "POST", "/login", `{"username":"bob","password":"Passw0rd1"}`)
	if code != http.StatusOK {
		t.Fatalf("登录 = %d", code)
	}
	var newSID string
	for _, ck := range cookies {
		if ck.Name == cookieName {
			newSID = ck.Value
		}
	}
	if newSID == "" || newSID == oldSID {
		t.Fatalf("登录后 SID = %q，必须是全新的（会话固定防御失败）", newSID)
	}
	// 旧 SID 已作废
	if _, err := store.Get(ctx, oldSID); err == nil {
		t.Error("旧 SID 仍有效——轮换未销毁旧会话")
	}
	// 新 SID 可用，且轮换标记可见
	sess, err := store.Get(ctx, newSID)
	if err != nil || sess.UserID != "bob" || !sess.Fixed {
		t.Errorf("新会话异常: %+v err=%v", sess, err)
	}
}

// TestLoginLogoutFlow：验收③的 API 面——登录→me→登出→me 401。
func TestLoginLogoutFlow(t *testing.T) {
	_, h := newTestAuth(t)
	jar := []*http.Cookie{}

	if code, _, _ := callAuth(t, h, jar, "GET", "/me", ""); code != http.StatusUnauthorized {
		t.Errorf("未登录 me = %d， want 401", code)
	}
	if code, _, _ := callAuth(t, h, jar, "POST", "/register", `{"username":"carol","password":"Passw0rd1"}`); code != http.StatusCreated {
		t.Fatalf("注册 = %d， want 201", code)
	}
	code, cookies, _ := callAuth(t, h, jar, "POST", "/login", `{"username":"carol","password":"Passw0rd1"}`)
	if code != http.StatusOK {
		t.Fatalf("登录 = %d", code)
	}
	jar = cookies
	for _, ck := range jar { // HttpOnly 是 Cookie 的安全底线
		if ck.Name == cookieName && !ck.HttpOnly {
			t.Error("会话 Cookie 缺 HttpOnly")
		}
	}
	code, _, body := callAuth(t, h, jar, "GET", "/me", "")
	if code != http.StatusOK {
		t.Fatalf("me = %d", code)
	}
	var me map[string]any
	if err := json.Unmarshal([]byte(body), &me); err != nil || me["user"] != "carol" {
		t.Errorf("me 响应异常: %s", body)
	}
	if code, _, _ = callAuth(t, h, jar, "POST", "/logout", ""); code != http.StatusOK {
		t.Fatalf("登出 = %d", code)
	}
	if code, _, _ = callAuth(t, h, jar, "GET", "/me", ""); code != http.StatusUnauthorized {
		t.Errorf("登出后 me = %d， want 401", code)
	}
}

// TestLoginMessageDoesNotLeak：统一 401 文案——错误密码与不存在用户同响应。
func TestLoginMessageDoesNotLeak(t *testing.T) {
	_, h := newTestAuth(t)
	_, _, body1 := callAuth(t, h, nil, "POST", "/login", `{"username":"ghost","password":"Whatever1"}`)
	_, _, body2 := callAuth(t, h, nil, "POST", "/login", `{"username":"carol","password":"WrongPass1"}`)
	if body1 != body2 {
		t.Errorf("两种失败文案不同（泄露用户存在性）:\n%s\n%s", body1, body2)
	}
}

// TestSessionTTL：会话写入即带 TTL——过期由 Redis 负责，无扫描任务。
func TestSessionTTL(t *testing.T) {
	store, _ := newTestAuth(t)
	ctx := context.Background()
	sid, err := store.Create(ctx, "dave", false)
	if err != nil {
		t.Fatal(err)
	}
	ttl, err := store.rdb.TTL(ctx, sessionKey(sid)).Result()
	if err != nil || ttl <= 0 || ttl > 24*time.Hour {
		t.Errorf("会话 TTL = %v err=%v， want (0, 24h]", ttl, err)
	}
}
