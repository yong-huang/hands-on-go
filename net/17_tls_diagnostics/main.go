// 项目 17：TLS 故障诊断手册——四种典型故障的一键复现与双口径诊断。
//
// 生产 TLS 故障来来去去就四类，本篇把它们全部做成可一键复现的现场，
// 并用 Go 客户端与 openssl s_client 两个口径采集证据：
//
//	fault 1  证书过期       ：NotAfter 设在过去
//	fault 2  SAN 错配       ：证书没有目标地址的 SAN
//	fault 3  链不完整       ：服务端只发叶子不发中间
//	fault 4  系统时钟偏差   ：客户端时间倒回一年（NotBefore 校验失败）
//
// 每个现场输出：Go 客户端错误（含错误链解包）+ openssl s_client 报错行，
// 最后打印一页诊断决策树（报错 → 最可能原因 → 验证命令）。
//
// 运行方式：go run .（约 2 秒，全部 loopback）；断言见 go test -race。
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CA：签发机构（与前两篇同构）。
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

func newCA(name string, serial int64) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: name, Organization: []string{"hands-on-net"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: cert, Key: key}, nil
}

func (ca *CA) signCert(tpl *x509.Certificate) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

func leafTemplate(cn string, serial int64, ips []net.IP, dns []string, nb, na time.Time) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"hands-on-net"}},
		NotBefore:             nb,
		NotAfter:              na,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           ips,
		DNSNames:              dns,
		BasicConstraintsValid: true,
	}
}

// startSrv：按指定证书链与 ClientAuth 关闭态起服务端（诊断只关注服务端身份）。
func startSrv(fullChain bool, leaf, inter tls.Certificate) (string, func()) {
	cfg := &tls.Config{}
	if fullChain {
		cfg.Certificates = []tls.Certificate{{ // 叶子 + 中间
			Certificate: [][]byte{leaf.Certificate[0], inter.Certificate[0]},
			PrivateKey:  leaf.PrivateKey,
			Leaf:        leaf.Leaf,
		}}
	} else {
		cfg.Certificates = []tls.Certificate{leaf} // 只发叶子（链不完整）
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				fmt.Fprintf(c, "ok\n")
				c.Close()
			}(c)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

// trustRoot：信任根的客户端配置（可定制时钟，模拟时钟偏差）。
func trustRoot(root *CA, now func() time.Time) *tls.Config {
	roots := x509.NewCertPool()
	roots.AddCert(root.Cert)
	return &tls.Config{RootCAs: roots, ServerName: "localhost", Time: now}
}

// goProbe：Go 客户端连一次，返回规范化后的错误链（类型名 + 每层消息）。
func goProbe(addr string, cfg *tls.Config) []string {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", addr, cfg)
	if err == nil {
		conn.Close()
		return []string{"(握手成功)"}
	}
	var out []string
	for e := err; e != nil; {
		out = append(out, fmt.Sprintf("%T: %v", e, e))
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	return out
}

// opensslProbe：外部视角——s_client 连一次，抽验证相关行。
func opensslProbe(addr string) []string {
	out, err := exec.Command("openssl", "s_client", "-connect", addr,
		"-brief").CombinedOutput()
	if err != nil && len(out) == 0 {
		return []string{"(openssl 不可用: " + err.Error() + ")"}
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "Verification") || strings.Contains(l, "verification") {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "(无 Verification 行——握手在更早阶段失败)")
	}
	return lines
}

func main() {
	root, err := newCA("diag Root CA", 4000)
	if err != nil {
		fmt.Println(err)
		return
	}
	inter, err := root.signCert(leafTemplate("diag Intermediate", 4001, nil, nil,
		time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour)))
	if err != nil {
		fmt.Println(err)
		return
	}
	// 给中间补 CA 属性（叶子模板没带 IsCA，重签一张真正的中间）
	interCAKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	interTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(4001),
		Subject:               pkix.Name{CommonName: "diag Intermediate CA", Organization: []string{"hands-on-net"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	interDer, err := x509.CreateCertificate(rand.Reader, interTpl, root.Cert, &interCAKey.PublicKey, root.Key)
	if err != nil {
		fmt.Println(err)
		return
	}
	interCA := &CA{Cert: mustParse(interDer), Key: interCAKey}

	srvLeaf, err := interCA.signCert(leafTemplate("diag-server", 4002,
		[]net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"},
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour)))
	if err != nil {
		fmt.Println(err)
		return
	}

	faults := []struct {
		name  string
		full  bool
		leaf  tls.Certificate
		nowFn func() time.Time
		hint  string
	}{
		{"故障 1：证书过期（NotAfter 在过去）", true, mustLeaf(interCA, 4003,
			[]net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"},
			time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)),
			nil, "换新证书；长期方案是自动化轮换（短有效期）"},
		{"故障 2：SAN 错配（证书无 127.0.0.1）", true, mustLeaf(interCA, 4004,
			nil, []string{"elsewhere.example"},
			time.Now().Add(-time.Hour), time.Now().Add(time.Hour)),
			nil, "按实际服务地址重签叶子（SAN 双写 IP+DNS）"},
		{"故障 3：链不完整（服务端只发叶子）", false, srvLeaf,
			nil, "服务端配置补上中间证书（叶子+中间一起发）"},
		{"故障 4：客户端时钟偏差（回拨一年）", true, srvLeaf,
			func() time.Time { return time.Now().Add(-365 * 24 * time.Hour) },
			"校准客户端系统时钟（NTP）；NotBefore 校验对时钟敏感"},
	}

	for i, f := range faults {
		fmt.Printf("== %s ==\n", f.name)
		addr, closeSrv := startSrv(f.full, f.leaf, inter)
		cfg := trustRoot(root, orNow(f.nowFn))
		fmt.Println("  [Go 客户端错误链]")
		for _, l := range goProbe(addr, cfg) {
			fmt.Println("    " + l)
		}
		fmt.Println("  [openssl s_client]")
		for _, l := range opensslProbe(addr) {
			fmt.Println("    " + l)
		}
		fmt.Println("  [处置] " + f.hint)
		_ = i
		closeSrv()
		fmt.Println()
	}
	printDecisionTree()
	_ = inter
}

func orNow(f func() time.Time) func() time.Time {
	if f != nil {
		return f
	}
	return time.Now
}

func mustParse(der []byte) *x509.Certificate {
	c, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return c
}

func mustLeaf(ca *CA, serial int64, ips []net.IP, dns []string, nb, na time.Time) tls.Certificate {
	c, err := ca.signCert(leafTemplate("fault-leaf", serial, ips, dns, nb, na))
	if err != nil {
		panic(err)
	}
	return c
}

// printDecisionTree：一页诊断决策树（报错 → 原因 → 验证命令）。
func printDecisionTree() { printDecisionTreeTo(os.Stdout) }

func printDecisionTreeTo(w io.Writer) {
	w2 := w
	_ = w2
	printDecisionTreeInner(func(s string) { fmt.Fprint(w, s) })
}

func printDecisionTreeInner(emit func(string)) {
	emit("== 诊断决策树（报错 → 最可能原因 → 验证命令）==\n")
	emit("  x509: certificate has expired ...\n")
	emit("      → 证书过期 → openssl s_client -connect h:443 看 NotAfter；换证书\n")
	emit("  x509: certificate is valid for X, not Y / doesn't contain any IP SANs\n")
	emit("      → SAN 错配 → openssl x509 -noout -ext subjectAltName；按服务地址重签\n")
	emit("  x509: certificate signed by unknown authority\n")
	emit("      → 链不完整（最常见）或信任池缺根 → openssl s_client 看 Certificate chain 是否含中间；服务端补中间证书\n")
	emit("  x509: ... is not yet valid（时钟正常但报 not yet valid）\n")
	emit("      → 时钟偏差 → date 比对两端时钟；NTP 校准\n")
	emit("  remote error: tls: certificate required / bad certificate\n")
	emit("      → mTLS 客户端证书问题 → 检查 ClientAuth 档位与客户端证书（项目 16）\n")
}

func printDecisionTreeOld() {
	fmt.Println("== 诊断决策树（报错 → 最可能原因 → 验证命令）==")
	fmt.Println("  x509: certificate has expired ...")
	fmt.Println("      → 证书过期 → openssl s_client -connect h:443 看 NotAfter；换证书")
	fmt.Println("  x509: certificate is valid for X, not Y / doesn't contain any IP SANs")
	fmt.Println("      → SAN 错配 → openssl x509 -noout -ext subjectAltName；按服务地址重签")
	fmt.Println("  x509: certificate signed by unknown authority")
	fmt.Println("      → 链不完整（最常见）或信任池缺根 → openssl s_client 看 Certificate chain 是否含中间；服务端补中间证书")
	fmt.Println("  x509: certificate has expired or is not yet valid（客户端时钟正常但报 not yet valid）")
	fmt.Println("      → 时钟偏差 → date 比对两端时钟；NTP 校准")
	fmt.Println("  remote error: tls: certificate required / bad certificate")
	fmt.Println("      → mTLS 客户端证书问题 → 检查 ClientAuth 档位与客户端证书（项目 16）")
}
