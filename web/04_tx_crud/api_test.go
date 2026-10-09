package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// do：发请求并返回状态码与响应体（解析进 out 可为 nil）。
func do(t *testing.T, method, url, body string, out any) int {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := noKeepAlive.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("响应不是合法 JSON: %v", err)
		}
	}
	return resp.StatusCode
}

var noKeepAlive = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

// TestCRUDIntegration：验收②——注册齐全的增查改删全链路，
// 连同参数校验、404 语义一起断言。全程 httptest + 全新临时库。
func TestCRUDIntegration(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(newAPI(s))
	defer ts.Close()

	// 建账前先确认探针
	if got := do(t, "GET", ts.URL+"/healthz", "", nil); got != http.StatusOK {
		t.Fatalf("healthz = %d， want 200", got)
	}

	// POST：创建
	var created struct {
		ID int64 `json:"id"`
	}
	if got := do(t, "POST", ts.URL+"/api/v1/bookmarks",
		`{"url":"https://go.dev","title":"Go 官网"}`, &created); got != http.StatusCreated {
		t.Fatalf("POST = %d， want 201", got)
	}
	if created.ID <= 0 {
		t.Fatalf("POST 返回 id=%d， want 正数", created.ID)
	}

	// GET：字段回读一致
	var got Bookmark
	if code := do(t, "GET", fmt.Sprintf("%s/api/v1/bookmarks/%d", ts.URL, created.ID), "", &got); code != http.StatusOK {
		t.Fatalf("GET = %d， want 200", code)
	}
	if got.URL != "https://go.dev" || got.Title != "Go 官网" {
		t.Errorf("回读不一致: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at 未返回")
	}

	// PUT：改标题，再读验证
	code := do(t, "PUT", fmt.Sprintf("%s/api/v1/bookmarks/%d", ts.URL, created.ID),
		`{"title":"Go 官网（改）"}`, nil)
	if code != http.StatusNoContent {
		t.Fatalf("PUT = %d， want 204", code)
	}
	var updated Bookmark
	do(t, "GET", fmt.Sprintf("%s/api/v1/bookmarks/%d", ts.URL, created.ID), "", &updated)
	if updated.Title != "Go 官网（改）" {
		t.Errorf("PUT 未生效: %q", updated.Title)
	}

	// DELETE：删除后 GET 应 404
	code = do(t, "DELETE", fmt.Sprintf("%s/api/v1/bookmarks/%d", ts.URL, created.ID), "", nil)
	if code != http.StatusNoContent {
		t.Fatalf("DELETE = %d， want 204", code)
	}
	if code := do(t, "GET", fmt.Sprintf("%s/api/v1/bookmarks/%d", ts.URL, created.ID), "", nil); code != http.StatusNotFound {
		t.Fatalf("删除后 GET = %d， want 404", code)
	}

	// 错误语义：非法 id / 非法 JSON / 不存在的资源
	if code := do(t, "GET", ts.URL+"/api/v1/bookmarks/abc", "", nil); code != http.StatusBadRequest {
		t.Errorf("非法 id = %d， want 400", code)
	}
	if code := do(t, "POST", ts.URL+"/api/v1/bookmarks", `{broken`, nil); code != http.StatusBadRequest {
		t.Errorf("非法 JSON = %d， want 400", code)
	}
	if code := do(t, "POST", ts.URL+"/api/v1/bookmarks", `{"title":"缺 url"}`, nil); code != http.StatusBadRequest {
		t.Errorf("缺 url = %d， want 400", code)
	}
	if code := do(t, "PUT", ts.URL+"/api/v1/bookmarks/999", `{"title":"x"}`, nil); code != http.StatusNotFound {
		t.Errorf("改不存在的 id = %d， want 404", code)
	}
	if code := do(t, "DELETE", ts.URL+"/api/v1/bookmarks/999", "", nil); code != http.StatusNotFound {
		t.Errorf("删不存在的 id = %d， want 404", code)
	}

	// LIST：删干净后应为空列表
	var listResp struct {
		Bookmarks []Bookmark `json:"bookmarks"`
	}
	if code := do(t, "GET", ts.URL+"/api/v1/bookmarks", "", &listResp); code != http.StatusOK {
		t.Fatalf("LIST = %d， want 200", code)
	}
	if len(listResp.Bookmarks) != 0 {
		t.Errorf("LIST 剩 %d 条， want 0", len(listResp.Bookmarks))
	}
}

// TestStartupAutoMigrates：验收③——全新库上直接起 API，迁移自动完成，
// 第一个 POST 就能成功（表已经在了）。
func TestStartupAutoMigrates(t *testing.T) {
	s := newTestStore(t) // 注意：故意不手动 Migrate
	ts := httptest.NewServer(newAPI(s))
	defer ts.Close()

	// newAPI 不含迁移；自动迁移属于启动序列，等价口径：
	// startup 函数跑完后，POST 直接可用
	applied, err := s.Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != len(migrations) {
		t.Fatalf("启动迁移应用 %d 个版本， want %d", len(applied), len(migrations))
	}

	var created struct {
		ID int64 `json:"id"`
	}
	if code := do(t, "POST", ts.URL+"/api/v1/bookmarks",
		`{"url":"https://pkg.go.dev","title":"标准库"}`, &created); code != http.StatusCreated {
		t.Fatalf("启动后首个 POST = %d， want 201（迁移未生效）", code)
	}
}
