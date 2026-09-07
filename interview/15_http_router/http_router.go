// Mini HTTP Router —— 基于 http.Handler 接口的路由器
// 面试高频题: 路由匹配、路径参数提取、中间件链、ServeHTTP 接口

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// ── 1. 核心数据结构 ──

// route 存储一条注册的路由规则
type route struct {
	method     string              // HTTP 方法: GET, POST, PUT, DELETE
	pattern    string              // 路径模式, 如 /users/:id/posts/:pid
	handler    http.HandlerFunc   // 处理函数
	paramNames []string            // 参数名列表, 如 ["id", "pid"]
}

// Router 实现 http.Handler 接口的自定义路由器
type Router struct {
	routes      []route
	middlewares []func(http.HandlerFunc) http.HandlerFunc
	mu          sync.RWMutex
}

// NewRouter 创建路由器实例
func NewRouter() *Router {
	return &Router{}
}

// ── 2. 路由注册 ──

// addRoute 注册路由（内部方法），解析 pattern 中的 :param 参数名
func (r *Router) addRoute(method, pattern string, handler http.HandlerFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()

	paramNames := extractParamNames(pattern)
	r.routes = append(r.routes, route{
		method:     method,
		pattern:    pattern,
		handler:    handler,
		paramNames: paramNames,
	})
}

// GET 注册 GET 路由
func (r *Router) GET(pattern string, handler http.HandlerFunc) {
	r.addRoute("GET", pattern, handler)
}

// POST 注册 POST 路由
func (r *Router) POST(pattern string, handler http.HandlerFunc) {
	r.addRoute("POST", pattern, handler)
}

// PUT 注册 PUT 路由
func (r *Router) PUT(pattern string, handler http.HandlerFunc) {
	r.addRoute("PUT", pattern, handler)
}

// DELETE 注册 DELETE 路由
func (r *Router) DELETE(pattern string, handler http.HandlerFunc) {
	r.addRoute("DELETE", pattern, handler)
}

// Use 添加中间件（按添加顺序执行）
func (r *Router) Use(middleware func(http.HandlerFunc) http.HandlerFunc) {
	r.middlewares = append(r.middlewares, middleware)
}

// ── 3. 路由匹配 ──

// extractParamNames 从 pattern 中提取所有 :param 参数名
// 例如: /users/:id/posts/:pid → ["id", "pid"]
func extractParamNames(pattern string) []string {
	var names []string
	for _, seg := range strings.Split(pattern, "/") {
		if strings.HasPrefix(seg, ":") {
			names = append(names, strings.TrimPrefix(seg, ":"))
		}
	}
	return names
}

// matchRoute 将实际路径与 pattern 匹配，提取路径参数
// 例如: pattern="/users/:id/posts/:pid", path="/users/42/posts/7"
//       → matched=true, params={"id": "42", "pid": "7"}
func matchRoute(pattern, path string, paramNames []string) (bool, map[string]string) {
	patSegs := strings.Split(pattern, "/")
	pathSegs := strings.Split(path, "/")

	if len(patSegs) != len(pathSegs) {
		return false, nil
	}

	params := make(map[string]string, len(paramNames))
	paramIdx := 0
	for i := range patSegs {
		if strings.HasPrefix(patSegs[i], ":") {
			// 动态参数段，匹配任意值
			if paramIdx < len(paramNames) {
				params[paramNames[paramIdx]] = pathSegs[i]
				paramIdx++
			}
		} else {
			// 静态段，必须精确匹配
			if patSegs[i] != pathSegs[i] {
				return false, nil
			}
		}
	}
	return true, params
}

// ── 4. ServeHTTP —— 实现 http.Handler 接口 ──

// ServeHTTP 是 Router 的核心方法，Go 的 net/http 包通过此方法分发请求
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 1. 遍历路由表查找匹配
	for _, rt := range r.routes {
		if rt.method != req.Method {
			continue
		}
		matched, params := matchRoute(rt.pattern, req.URL.Path, rt.paramNames)
		if !matched {
			continue
		}

		// 2. 将路径参数存入 request context，后续 handler 可通过 Param() 取出
		ctx := req.Context()
		for k, v := range params {
			ctx = contextWithParam(ctx, k, v)
		}
		req = req.WithContext(ctx)

		// 3. 包装 handler：先跑中间件链，再执行业务 handler
		handler := rt.handler
		// 中间件从后往前包裹，保证执行顺序与注册顺序一致
		for i := len(r.middlewares) - 1; i >= 0; i-- {
			handler = r.middlewares[i](handler)
		}
		handler(w, req)
		return
	}

	// 4. 无匹配路由 → 404
	http.NotFound(w, req)
}

// ── 5. Context 键值存储路径参数 ──

type contextKey string

const paramsKey contextKey = "routeParams"

func contextWithParam(ctx context.Context, key, value string) context.Context {
	params, _ := ctx.Value(paramsKey).(map[string]string)
	if params == nil {
		params = make(map[string]string)
	}
	params[key] = value
	return context.WithValue(ctx, paramsKey, params)
}

// Param 从 request context 中提取路径参数
func Param(r *http.Request, name string) string {
	params, _ := r.Context().Value(paramsKey).(map[string]string)
	return params[name]
}

// ── 6. 演示 ──

func runDemo() {
	fmt.Println("=== Mini HTTP Router Demo ===\n")

	router := NewRouter()

	// 1. 注册中间件
	router.Use(loggingMiddleware)
	router.Use(authMiddleware)

	// 2. 注册路由
	router.GET("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Welcome to Home!")
	})
	router.GET("/users/:id", func(w http.ResponseWriter, r *http.Request) {
		id := Param(r, "id")
		fmt.Fprintf(w, "Get User: id=%s", id)
	})
	router.GET("/users/:id/posts/:pid", func(w http.ResponseWriter, r *http.Request) {
		id := Param(r, "id")
		pid := Param(r, "pid")
		fmt.Fprintf(w, "Get Post: userId=%s, postId=%s", id, pid)
	})
	router.POST("/users", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Create User")
	})
	router.PUT("/users/:id", func(w http.ResponseWriter, r *http.Request) {
		id := Param(r, "id")
		fmt.Fprintf(w, "Update User: id=%s", id)
	})
	router.DELETE("/users/:id", func(w http.ResponseWriter, r *http.Request) {
		id := Param(r, "id")
		fmt.Fprintf(w, "Delete User: id=%s", id)
	})
	router.GET("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "OK")
	})

	// 3. 打印路由表
	fmt.Println("--- Routing Table ---")
	fmt.Printf("%-6s %-30s %s\n", "Method", "Pattern", "ParamNames")
	fmt.Println(strings.Repeat("-", 60))
	router.mu.RLock()
	for _, rt := range router.routes {
		fmt.Printf("%-6s %-30s %v\n", rt.method, rt.pattern, rt.paramNames)
	}
	router.mu.RUnlock()
	fmt.Println()

	// 4. 用 httptest 发送模拟请求
	fmt.Println("--- Fake Requests ---")
	testCases := []struct {
		method string
		path   string
	}{
		{"GET", "/"},
		{"GET", "/users/42"},
		{"GET", "/users/42/posts/7"},
		{"POST", "/users"},
		{"PUT", "/users/99"},
		{"DELETE", "/users/99"},
		{"GET", "/health"},
		{"GET", "/notfound"},
	}

	for _, tc := range testCases {
		fmt.Printf("\n  %s %s\n", tc.method, tc.path)
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		fmt.Printf("    Status: %d\n", rec.Code)
		fmt.Printf("    Body:   %s\n", strings.TrimSpace(rec.Body.String()))
	}

	fmt.Println()
	fmt.Println("--- Param Extraction Demo ---")
	demoParamExtraction()
}

func demoParamExtraction() {
	cases := []struct {
		pattern string
		path    string
	}{
		{"/users/:id", "/users/123"},
		{"/users/:id/posts/:pid", "/users/42/posts/7"},
		{"/users/:id/friends/:fid", "/users/1/friends/5"},
		{"/static/a", "/static/b"}, // 不匹配
	}
	for _, c := range cases {
		names := extractParamNames(c.pattern)
		ok, params := matchRoute(c.pattern, c.path, names)
		if ok {
			fmt.Printf("  %s  ×  %s  →  %v\n", c.pattern, c.path, params)
		} else {
			fmt.Printf("  %s  ×  %s  →  NO MATCH\n", c.pattern, c.path)
		}
	}
}

// ── 7. 中间件示例 ──

func loggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("    [LOG] %s %s\n", r.Method, r.URL.Path)
		next(w, r)
	}
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 模拟鉴权：/health 跳过
		if r.URL.Path != "/health" {
			fmt.Printf("    [AUTH] checking token for %s\n", r.URL.Path)
		}
		next(w, r)
	}
}

// ── 8. 主入口 ──

func main() {
	runDemo()
}
