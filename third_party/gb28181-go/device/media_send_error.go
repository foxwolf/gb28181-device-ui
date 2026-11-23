package device

import (
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
)

// Windows WSA codes used by TCP send failures (not defined on all GOOS).
const (
	wsaEConnAborted syscall.Errno = 10053
	wsaEConnReset   syscall.Errno = 10054
	wsaENOTCONN     syscall.Errno = 10057
	wsaESHUTDOWN    syscall.Errno = 10058
)

// isUnrecoverableMediaSendError reports whether an RTP send error means the
// transport is dead and retrying every subsequent frame cannot succeed.
//
// TCP: peer close/reset/abort, broken pipe, or a closed local socket.
// UDP: only a closed local socket (a missing peer does not error WriteToUDP,
// so a one-off send error is not treated as session death).
func isUnrecoverableMediaSendError(err error, overTCP bool) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	if overTCP && errors.Is(err, io.EOF) {
		return true
	}

	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE, syscall.ENOTCONN:
			return true
		case wsaEConnAborted, wsaEConnReset, wsaENOTCONN, wsaESHUTDOWN:
			return true
		}
	}

	if !overTCP {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "connection was aborted") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "forcibly closed") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "not connected") {
		return true
	}
	return false
}

// stopCurrentMediaIf tears down the live media session only if tcpConn is
// still the server's current TCP media socket (a newer INVITE may have
// already replaced it). Caller may be the media goroutine.
func (s *Server) stopCurrentMediaIf(tcpConn *net.TCPConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tcpConn != nil && s.mediaTCPConn != nil && s.mediaTCPConn != tcpConn {
		return
	}
	if tcpConn != nil && s.mediaTCPConn == nil {
		// Already torn down (BYE or a replacing INVITE).
		return
	}
	s.teardownMediaLocked()
}
