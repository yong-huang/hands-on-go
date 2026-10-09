package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

const testDB = "marklet12"

func testDBConn(t *testing.T) *sql.DB {
	t.Helper()
	if err := EnsurePGDatabase(testDB); err != nil {
		t.Skipf("PG 不可用（../05_pg_migration/pg.sh start 可启动）：%v", err)
	}
	db, err := OpenDB(withDB(testDB))
	if err != nil {
		t.Skipf("PG 不可用：%v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := EnsureSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	// 清空数据：每个用例从零开始（建表在前，清库在后）
	for _, ddl := range []string{`DELETE FROM bookmarks`, `DELETE FROM users`} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("清库: %v", err)
		}
	}
	return db
}

// client：带令牌罐的测试客户端（httptest 直连 handler，不经网络）。
type client struct {
	t     *testing.T
	h     http.Handler
	token string
}

func (c *client) do(method, path, body string) (int, string) {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (c *client) registerAndLogin(user string) {
	c.t.Helper()
	if code, _ := c.do("POST", "/api/v1/auth/register",
		fmt.Sprintf(`{"username":%q,"password":"Passw0rd1"}`, user)); code != http.StatusCreated {
		c.t.Fatalf("注册 %s = %d", user, code)
	}
	code, body := c.do("POST", "/api/v1/auth/login",
		fmt.Sprintf(`{"username":%q,"password":"Passw0rd1"}`, user))
	if code != http.StatusOK {
		c.t.Fatalf("登录 %s = %d %s", user, code, body)
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	c.token = out.Token
}

// TestFullFlow：验收③——注册→登录→建→查→改→删 全链路。
func TestFullFlow(t *testing.T) {
	db := testDBConn(t)
	c := &client{t: t, h: newWiredAPI(db)}
	c.registerAndLogin("alice")

	code, body := c.do("POST", "/api/v1/bookmarks", `{"url":"https://go.dev","title":"Go 官网"}`)
	if code != http.StatusCreated {
		t.Fatalf("建 = %d %s", code, body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &created)

	if code, body = c.do("GET", fmt.Sprintf("/api/v1/bookmarks/%d", created.ID), ""); code != http.StatusOK {
		t.Fatalf("查 = %d %s", code, body)
	}
	if code, _ = c.do("PUT", fmt.Sprintf("/api/v1/bookmarks/%d", created.ID), `{"title":"改名"}`); code != http.StatusNoContent {
		t.Fatalf("改 = %d", code)
	}
	if _, body = c.do("GET", fmt.Sprintf("/api/v1/bookmarks/%d", created.ID), ""); !strings.Contains(body, "改名") {
		t.Errorf("改后回读: %s", body)
	}
	if code, _ = c.do("DELETE", fmt.Sprintf("/api/v1/bookmarks/%d", created.ID), ""); code != http.StatusNoContent {
		t.Fatalf("删 = %d", code)
	}
	if code, _ = c.do("GET", fmt.Sprintf("/api/v1/bookmarks/%d", created.ID), ""); code != http.StatusNotFound {
		t.Errorf("删后查 = %d， want 404", code)
	}
}

// TestIDOR：验收①——用户 A 对用户 B 的书签 GET/PUT/DELETE 全部 404
// （与"真不存在"同响应，不泄露存在性）。
func TestIDOR(t *testing.T) {
	db := testDBConn(t)
	h := newWiredAPI(db)
	alice := &client{t: t, h: h}
	mallory := &client{t: t, h: h}
	alice.registerAndLogin("alice")
	mallory.registerAndLogin("mallory")

	code, body := alice.do("POST", "/api/v1/bookmarks", `{"url":"https://go.dev","title":"Alice 的书签"}`)
	if code != http.StatusCreated {
		t.Fatalf("建 = %d %s", code, body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &created)
	path := fmt.Sprintf("/api/v1/bookmarks/%d", created.ID)

	for _, tc := range []struct{ method string }{
		{"GET"}, {"PUT"}, {"DELETE"},
	} {
		code, _ = mallory.do(tc.method, path, `{"title":"hacked"}`)
		if code != http.StatusNotFound {
			t.Errorf("mallory %s = %d， want 404（IDOR 防御失败）", tc.method, code)
		}
	}
	// 数据毫发无损：alice 仍能读到
	if code, _ = alice.do("GET", path, ""); code != http.StatusOK {
		t.Errorf("alice 自己读 = %d， want 200", code)
	}
}

// TestListIsolation：列表按 owner 隔离——mallory 的列表里没有 alice 的书签。
func TestListIsolation(t *testing.T) {
	db := testDBConn(t)
	h := newWiredAPI(db)
	alice := &client{t: t, h: h}
	mallory := &client{t: t, h: h}
	alice.registerAndLogin("alice")
	mallory.registerAndLogin("mallory")

	if code, _ := alice.do("POST", "/api/v1/bookmarks", `{"url":"https://alice.dev","title":"A"}`); code != http.StatusCreated {
		t.Fatal("alice 建书签失败")
	}
	if code, _ := mallory.do("POST", "/api/v1/bookmarks", `{"url":"https://mallory.dev","title":"M"}`); code != http.StatusCreated {
		t.Fatal("mallory 建书签失败")
	}

	code, body := mallory.do("GET", "/api/v1/bookmarks", "")
	if code != http.StatusOK {
		t.Fatalf("mallory 列表 = %d", code)
	}
	if strings.Contains(body, "alice.dev") {
		t.Errorf("mallory 的列表混入了 alice 的书签: %s", body)
	}
	var out struct {
		Bookmarks []Bookmark `json:"bookmarks"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	if len(out.Bookmarks) != 1 || out.Bookmarks[0].URL != "https://mallory.dev" {
		t.Errorf("mallory 列表异常: %s", body)
	}
}

// TestUnauthenticatedRejected：验收②——未登录与伪造令牌，受保护组全量 401。
func TestUnauthenticatedRejected(t *testing.T) {
	db := testDBConn(t)
	h := newWiredAPI(db)
	noAuth := &client{t: t, h: h}
	forbidden := []struct {
		method, path, body string
	}{
		{"GET", "/api/v1/bookmarks", ""},
		{"POST", "/api/v1/bookmarks", `{"url":"https://x.dev"}`},
		{"GET", "/api/v1/bookmarks/1", ""},
		{"PUT", "/api/v1/bookmarks/1", `{"title":"x"}`},
		{"DELETE", "/api/v1/bookmarks/1", ""},
	}
	for _, tc := range forbidden {
		if code, _ := noAuth.do(tc.method, tc.path, tc.body); code != http.StatusUnauthorized {
			t.Errorf("未登录 %s %s = %d， want 401", tc.method, tc.path, code)
		}
	}
	forged := &client{t: t, h: h, token: mustForged()}
	if code, _ := forged.do("GET", "/api/v1/bookmarks", ""); code != http.StatusUnauthorized {
		t.Errorf("伪造令牌 = %d， want 401", code)
	}
	// 公开组不受影响
	if code, _ := noAuth.do("GET", "/healthz", ""); code != http.StatusOK {
		t.Errorf("healthz = %d， want 200（豁免失效）", code)
	}
}
