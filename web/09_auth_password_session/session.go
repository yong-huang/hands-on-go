package main

// session.go：Redis 会话存储——SID 生成、TTL 过期、登录轮换（防会话固定）。
//
// 会话（session）= 服务端保存的登录状态，客户端只持有一个不透明 ID（SID）。
// SID 必须不可预测：crypto/rand 128 位。会话固定攻击（session fixation）指
// 攻击者先把一个已知 SID 塞给受害者浏览器，等受害者登录后沿用该 SID 冒充——
// 防御只有一条铁律：登录成功的瞬间废弃旧 SID、签发全新 SID（轮换）。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNoSession：SID 无效/过期/已登出的统一语义。
var ErrNoSession = errors.New("no session")

// Session：会话载荷。
type Session struct {
	UserID  string    `json:"user_id"`
	LoginAt time.Time `json:"login_at"`
	Fixed   bool      `json:"fixed"` // 登录后轮换的标记（演示可观测）
}

// SessionStore：Redis 会话仓。TTL 即过期——不需要任何扫描任务。
type SessionStore struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewSessionStore：连接 Redis（池配置同实验 07 的教训）。
func NewSessionStore(addr string) (*SessionStore, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr: addr, PoolSize: 100,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, errors.New("redis 不可用（先 docker start marklet-redis）: " + err.Error())
	}
	return &SessionStore{rdb: rdb, ttl: 24 * time.Hour}, nil
}

func (s *SessionStore) Close() error { return s.rdb.Close() }

func sessionKey(sid string) string { return "sess:" + sid }

// newSID：128 位 crypto/rand → 32 个十六进制字符。会话 ID 的全部安全性
// 就在"攻击者猜不到"这一件事上。
func newSID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Create：签发新会话。TTL 即过期时间——Redis 替我们做了全部的过期管理。
func (s *SessionStore) Create(ctx context.Context, userID string, fixed bool) (string, error) {
	sid, err := newSID()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(Session{UserID: userID, LoginAt: time.Now().UTC(), Fixed: fixed})
	if err != nil {
		return "", err
	}
	if err := s.rdb.Set(ctx, sessionKey(sid), payload, s.ttl).Err(); err != nil {
		return "", err
	}
	return sid, nil
}

// Get：取会话；SID 不存在/过期统一返回 ErrNoSession（不区分——不给探测空间）。
func (s *SessionStore) Get(ctx context.Context, sid string) (*Session, error) {
	payload, err := s.rdb.Get(ctx, sessionKey(sid)).Bytes()
	if err != nil {
		return nil, ErrNoSession
	}
	var sess Session
	if err := json.Unmarshal(payload, &sess); err != nil {
		return nil, ErrNoSession
	}
	return &sess, nil
}

// Destroy：登出即删除。
func (s *SessionStore) Destroy(ctx context.Context, sid string) error {
	return s.rdb.Del(ctx, sessionKey(sid)).Err()
}

// Users：实验用内存用户表（真实 marklet 的用户表归 PG，实验 12 接入）。
type Users struct {
	mu map[string]string // username -> argon2id PHC
}

func NewUsers() *Users { return &Users{mu: map[string]string{}} }

func (u *Users) Register(username, password string) error {
	if _, ok := u.mu[username]; ok {
		return fmt.Errorf("用户已存在")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	u.mu[username] = hash
	return nil
}

func (u *Users) Verify(username, password string) (bool, error) {
	hash, ok := u.mu[username]
	if !ok {
		// 用户不存在也走一次哈希验证：让"用户不存在"与"密码错误"耗时一致，
		// 防止枚举用户名的计时侧信道
		_, _ = HashPassword(password)
		return false, nil
	}
	return VerifyPassword(password, hash)
}
