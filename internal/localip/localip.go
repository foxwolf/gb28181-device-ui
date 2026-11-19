// Package localip 复现 gb28181-go 未导出的 getLocalIP：对平台地址做一次 UDP
// connect（不真正发报文），再用 getsockname 得到本机出发地址。
package localip

import (
	"context"
	"fmt"
	"net"
	"strconv"
)

// Detect 返回访问 host:port 时内核会选用的本机源 IP。
func Detect(ctx context.Context, host string, port int) (string, error) {
	host = trim(host)
	if host == "" {
		return "", fmt.Errorf("平台地址为空")
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("平台端口无效: %d", port)
	}
	remote := net.JoinHostPort(host, strconv.Itoa(port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", remote)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	la, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || la.IP == nil {
		return "", fmt.Errorf("无法解析本机地址 %v", conn.LocalAddr())
	}
	return la.IP.String(), nil
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
