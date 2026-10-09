package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/goleak"
)

// goleak 门禁。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestSignAndParse：正常签发→验证回路，claims 里的 sub/iss 可回读。
func TestSignAndParse(t *testing.T) {
	token, err := Sign("alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(token, "."); len(got) != 3 {
		t.Fatalf("token 段数 = %d， want 3", len(got))
	}
	user, err := Parse(token)
	if err != nil {
		t.Fatalf("解析: %v", err)
	}
	if user != "alice" {
		t.Errorf("user = %s， want alice", user)
	}
}

// TestExpiredRejected：验收①——过期令牌必须被拒（exp 由库自动校验）。
func TestExpiredRejected(t *testing.T) {
	token, err := Sign("alice", -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(token); err == nil {
		t.Fatal("过期令牌被放行")
	}
}

// TestTamperedPayloadRejected：验收②——改 payload 不重签，签名必须对不上。
func TestTamperedPayloadRejected(t *testing.T) {
	token, err := Sign("alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tampered, err := TamperPayload(token, "mallory")
	if err != nil {
		t.Fatal(err)
	}
	if tampered == token {
		t.Fatal("篡改前后相同")
	}
	// 先证明 payload 确实可读可改（JWT 不是加密的教学点）
	parts := strings.Split(tampered, ".")
	raw, err := jwt.NewParser().DecodeSegment(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["sub"] != "mallory" {
		t.Fatalf("篡改未生效: %v", claims["sub"])
	}
	if _, err := Parse(tampered); err == nil {
		t.Fatal("篡改令牌被放行——签名校验失效")
	}
}

// TestAlgNoneRejected：验收③——alg=none 令牌必须被拒（白名单 + 库默认双保险）。
func TestAlgNoneRejected(t *testing.T) {
	none := CraftAlgNone("mallory")
	if _, err := Parse(none); err == nil {
		t.Fatal("alg=none 令牌被放行（白名单失效）")
	}
	// 就算去掉白名单，golang-jwt 默认解析器同样拒绝 none——库默认安全
	_, err := jwt.NewParser().Parse(none, func(*jwt.Token) (any, error) { return Secret, nil })
	if err == nil {
		t.Fatal("默认解析器放行了 alg=none")
	}
}

// TestWrongSecretRejected：换密钥签的令牌（等价于攻击者自签）必须被拒。
func TestWrongSecretRejected(t *testing.T) {
	old := Secret
	Secret = []byte("attacker-owned-key-0123456789abcdef")
	forged, err := Sign("mallory", time.Minute)
	Secret = old
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(forged); err == nil {
		t.Fatal("攻击者自签令牌被放行")
	}
}

// TestRefreshFlow：验收④——刷新换发新令牌且用户不变；
// 同时断言无状态的本质：旧令牌在 exp 前依然有效（没有吊销）。
func TestRefreshFlow(t *testing.T) {
	old, err := Sign("alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Refresh(old, 10*time.Minute)
	if err != nil {
		t.Fatalf("刷新: %v", err)
	}
	if user, err := Parse(fresh); err != nil || user != "alice" {
		t.Errorf("新令牌 user=%s err=%v", user, err)
	}
	if user, err := Parse(old); err != nil || user != "alice" {
		t.Errorf("旧令牌仍应有效（无状态无吊销）: user=%s err=%v", user, err)
	}
	// 无效旧令牌不能刷新
	expired, _ := Sign("alice", -time.Second)
	if _, err := Refresh(expired, time.Minute); err == nil {
		t.Error("过期令牌完成了刷新")
	}
}

// TestAPIBearer：HTTP 面——换令牌、带 Bearer 200、无令牌 401、乱令牌 401。
func TestAPIBearer(t *testing.T) {
	h := newAPI()
	srv := httptest.NewServer(h)
	defer srv.Close()

	post := func(path, body string) map[string]any {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	get := func(path, bearer string) int {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	out := post("/api/v1/auth/token", `{"username":"alice","password":"Passw0rd1"}`)
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatalf("换令牌失败: %v", out)
	}
	if code := get("/api/v1/me", token); code != http.StatusOK {
		t.Errorf("带令牌 me = %d， want 200", code)
	}
	if code := get("/api/v1/me", ""); code != http.StatusUnauthorized {
		t.Errorf("无令牌 me = %d， want 401", code)
	}
	if code := get("/api/v1/me", "garbage.token.value"); code != http.StatusUnauthorized {
		t.Errorf("乱令牌 me = %d， want 401", code)
	}
	if bad := post("/api/v1/auth/token", `{"username":"alice","password":"nope"}`); bad["token"] != nil {
		t.Error("错误凭证换到了令牌")
	}
}
