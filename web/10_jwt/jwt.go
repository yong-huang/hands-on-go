package main

// jwt.go：HS256 签发/验证 + 过期与刷新。
//
// JWT（JSON Web Token）= base64(header).base64(payload).base64(HMAC 签名)。
// 签名用只有服务端知道的密钥算出——payload 任何人都能解码阅读（不是加密！），
// 但改一个字节签名就对不上。无状态是它的全部价值与全部代价：
// 服务端不存任何东西，因此"吊销"天生困难（对照实验 09 的会话）。
//
// 三个安全要点全部落在 Parse 的选项里：
//   - WithValidMethods(["HS256"])：算法白名单——alg=none 与算法混淆攻击的闸门
//   - exp 校验由库默认执行（RegisteredClaims.ExpiresAt）
//   - 密钥强度：HMAC-SHA256 的安全性=密钥的熵，弱密钥可被离线爆破

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalid：无效 token 的统一出口（过期/篡改/alg 混淆/签名错 全部归一，
// 对外不泄露失败原因的细节——与实验 09 的登录文案同一纪律）。
var ErrInvalid = errors.New("invalid token")

// Claims：marklet 的令牌载荷。sub=用户名，exp/iat 由 RegisteredClaims 承担。
type Claims struct {
	jwt.RegisteredClaims
}

// Secret：HS256 密钥。演示用固定值——生产必须来自配置/密钥管理，
// 且长度 ≥32 字节（256 位）才配得上 SHA-256。
var Secret = []byte("marklet-demo-secret-key-0123456789abcdef")

// Sign：签发 HS256 token。ttl 即有效期（exp 由库写进 payload）。
func Sign(userID string, ttl time.Duration) (string, error) {
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			Issuer:    "marklet",
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(Secret)
}

// Parse：验证并取出载荷。三个陷阱的闸门都在这里：
// 算法白名单钉死 HS256（alg=none / RS-HS 混淆进不来），exp 由库自动校验，
// 签名不符直接拒绝。任何失败统一返回 ErrInvalid。
func Parse(tokenStr string) (string, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims,
		func(t *jwt.Token) (any, error) { return Secret, nil },
		jwt.WithValidMethods([]string{"HS256"}), // 算法白名单：安全闸门
		jwt.WithIssuer("marklet"),
	)
	if err != nil {
		return "", ErrInvalid
	}
	if claims.Subject == "" {
		return "", ErrInvalid
	}
	return claims.Subject, nil
}

// Refresh：刷新——旧 token 仍有效时换发新 token。
// 无状态的代价在这里显形：旧 token 在其 exp 之前始终可用（服务端没有"吊销"），
// 刷新并不能作废旧令牌，只是让客户端续命。
func Refresh(oldToken string, ttl time.Duration) (string, error) {
	user, err := Parse(oldToken)
	if err != nil {
		return "", err
	}
	return Sign(user, ttl)
}

// CraftAlgNone：构造 alg=none 的攻击令牌（教学用）——header 换成 none、
// 签名留空。正确配置的解析器必须拒绝它。
func CraftAlgNone(userID string) string {
	header := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"sub": userID, "iss": "marklet"})
	s, _ := header.SignedString(jwt.UnsafeAllowNoneSignatureType)
	return s
}

// TamperPayload：篡改 payload 的 sub 字段后原样拼回（不重签签名）——教学用。
// 注意 payload 只是 base64：任何人都能改，但改完的签名对不上。
func TamperPayload(tokenStr, newSub string) (string, error) {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("bad token shape")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims jwt.MapClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return "", err
	}
	claims["sub"] = newSub
	enc, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	return parts[0] + "." + base64.RawURLEncoding.EncodeToString(enc) + "." + parts[2], nil
}
