package main

// password.go：argon2id 密码哈希与弱密码策略。
//
// argon2id（2015 年密码哈希竞赛冠军，内存困难型）是当前密码存储的事实标准：
// 内存困难让 GPU/ASIC 的并行爆破失去经济性。存储采用 PHC 字符串格式：
//
//	$argon2id$v=19$m=32768,t=2,p=2$<base64盐>$<base64哈希>
//
// 盐（salt，每密码随机的字节数）直接编进字符串——同一密码两次哈希必然不同，
// 验证时从字符串里解析参数，不需要单独存盐列。

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
)

// argonParams：哈希参数（可按硬件调档；测试用轻量档保速度）。
// m=32MB 内存、t=2 轮、p=2 并行——单次哈希约 20-50ms，登录路径完全可接受。
type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
	keyLen  uint32
	saltLen uint32
}

var defaultParams = argonParams{
	memory: 32 * 1024, time: 2, threads: 2, keyLen: 32, saltLen: 16,
}

// ErrWeakPassword：弱密码策略拒绝的哨兵错误（文案随规则给出）。
var ErrWeakPassword = errors.New("weak password")

// HashPassword：密码 → PHC 字符串。盐取 crypto/rand——math/rand 的随机性
// 对攻击者是可预测的，密码学场景一律 crypto/rand。
func HashPassword(password string) (string, error) {
	salt := make([]byte, defaultParams.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt,
		defaultParams.time, defaultParams.memory, defaultParams.threads, defaultParams.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, defaultParams.memory, defaultParams.time, defaultParams.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash)), nil
}

// VerifyPassword：恒定时间比较——逐字节对比用时与差异位置无关，
// 防止计时侧信道（早退的比较会泄露"前几位对不对"）。
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("malformed hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, err
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// CheckPasswordPolicy：弱密码策略。返回违规原因切片（空 = 通过）。
// 规则刻意朴素：长度下限 + 必须含字母与数字。真实策略还会查泄露库（HIBP）。
func CheckPasswordPolicy(password string) []string {
	var violations []string
	if len(password) < 8 {
		violations = append(violations, "长度不足 8 位")
	}
	var hasLetter, hasDigit bool
	for _, r := range password {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	if !hasLetter {
		violations = append(violations, "未包含字母")
	}
	if !hasDigit {
		violations = append(violations, "未包含数字")
	}
	return violations
}
