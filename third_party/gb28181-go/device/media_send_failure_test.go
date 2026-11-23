package device

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestIsUnrecoverableMediaSendError(t *testing.T) {
	tcpWrap := func(err error) error {
		return fmt.Errorf("failed to send RTP packet fragment 0 over TCP: %w", err)
	}
	cases := []struct {
		name    string
		err     error
		overTCP bool
		want    bool
	}{
		{name: "nil", err: nil, overTCP: true, want: false},
		{name: "generic timeout tcp", err: errors.New("i/o timeout"), overTCP: true, want: false},
		{name: "generic udp", err: errors.New("i/o timeout"), overTCP: false, want: false},
		{name: "net.ErrClosed tcp", err: net.ErrClosed, overTCP: true, want: true},
		{name: "net.ErrClosed udp", err: net.ErrClosed, overTCP: false, want: true},
		{name: "closed pipe tcp", err: io.ErrClosedPipe, overTCP: true, want: true},
		{name: "eof tcp", err: io.EOF, overTCP: true, want: true},
		{name: "eof udp", err: io.EOF, overTCP: false, want: false},
		{name: "ECONNRESET wrapped tcp", err: tcpWrap(&net.OpError{Op: "write", Err: syscall.ECONNRESET}), overTCP: true, want: true},
		{name: "EPIPE wrapped tcp", err: tcpWrap(&net.OpError{Op: "write", Err: syscall.EPIPE}), overTCP: true, want: true},
		{name: "WSAECONNABORTED tcp", err: tcpWrap(&net.OpError{Op: "write", Err: syscall.Errno(10053)}), overTCP: true, want: true},
		{name: "WSAECONNRESET tcp", err: tcpWrap(&net.OpError{Op: "write", Err: syscall.Errno(10054)}), overTCP: true, want: true},
		{name: "windows abort text tcp", err: errors.New("write tcp 127.0.0.1:57644->127.0.0.1:36500: wsasend: An established connection was aborted by the software in your host machine."), overTCP: true, want: true},
		{name: "windows abort text not fatal for udp", err: errors.New("write tcp 127.0.0.1:57644->127.0.0.1:36500: wsasend: An established connection was aborted by the software in your host machine."), overTCP: false, want: false},
		{name: "forcibly closed tcp", err: errors.New("wsarecv: An existing connection was forcibly closed by the remote host."), overTCP: true, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUnrecoverableMediaSendError(tc.err, tc.overTCP); got != tc.want {
				t.Fatalf("got %v want %v for %v", got, tc.want, tc.err)
			}
		})
	}
}

type logCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func (l *logCounter) Enabled(context.Context, slog.Level) bool { return true }
func (l *logCounter) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts == nil {
		l.counts = map[string]int{}
	}
	l.counts[r.Message]++
	return nil
}
func (l *logCounter) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logCounter) WithGroup(string) slog.Handler      { return l }
func (l *logCounter) get(msg string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.counts[msg]
}

func hookLogs(t *testing.T) *logCounter {
	t.Helper()
	h := &logCounter{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

func playSDPTCP(mediaPort int) string {
	return "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=Play\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=video " + strconv.Itoa(mediaPort) + " TCP/RTP/AVP 96\r\na=setup:passive\r\na=connection:new\r\ny=2000000001\r\n"
}

func playSDPUDP(mediaPort int) string {
	return "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=Play\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=video " + strconv.Itoa(mediaPort) + " RTP/AVP 96\r\na=rtpmap:96 PS/90000\r\na=recvonly\r\ny=2000000001\r\n"
}

func sendInviteWithCallID(t *testing.T, sipPort int, callID, sdpBody string) string {
	t.Helper()
	invite := buildInvite(
		callID,
		"<sip:34020000002000000001@3402000000>;tag=plat",
		"<sip:34020000001320000001@3402000000>",
		"<sip:34020000002000000001@127.0.0.1:5060>",
	)
	invite.Body = sdpBody
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: sipPort})
	if err != nil {
		t.Fatalf("dial SIP: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write(invite.Serialize()); err != nil {
		t.Fatalf("send INVITE: %v", err)
	}
	buf := make([]byte, 65535)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return string(buf[:n])
}

func acceptTCP(t *testing.T, ln net.Listener, timeout time.Duration) net.Conn {
	t.Helper()
	ln.(*net.TCPListener).SetDeadline(time.Now().Add(timeout))
	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	return conn
}

func feedKeyframes(ctx context.Context, hub *FrameHub, every time.Duration) {
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				hub.Write(AccessUnit{
					NALUs:     []NALU{{Type: 5, Data: []byte{0x65, 0x88, 0x84, 0x21, 0xa0}, IsIDR: true}},
					KeyFrame:  true,
					Timestamp: time.Now(),
				})
			}
		}
	}()
}

func waitSubscribed(t *testing.T, hub *FrameHub, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if hub.SubscriberCount() > 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("device never subscribed")
}

func waitSubscribers(t *testing.T, hub *FrameHub, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if hub.SubscriberCount() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("subscriber count=%d want %d", hub.SubscriberCount(), want)
}

func readFramedRTP(t *testing.T, conn net.Conn, timeout time.Duration) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	rd := bufio.NewReader(conn)
	head := make([]byte, 5)
	if _, err := ioReadFullBuf(rd, head); err != nil {
		t.Fatalf("read framed RTP: %v", err)
	}
	if head[0] != 0x24 {
		t.Fatalf("framing byte: want 0x24, got %#x", head[0])
	}
}

func sendByeOK(t *testing.T, sipPort int, callID string) string {
	t.Helper()
	bye := buildBye(
		callID,
		"<sip:34020000002000000001@3402000000>;tag=plat",
		"<sip:34020000001320000001@3402000000>",
		"<sip:34020000002000000001@127.0.0.1:5060>",
	)
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: sipPort})
	if err != nil {
		t.Fatalf("dial SIP: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write(bye.Serialize()); err != nil {
		t.Fatalf("send BYE: %v", err)
	}
	buf := make([]byte, 65535)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read BYE response: %v", err)
	}
	resp := string(buf[:n])
	if !strings.Contains(resp, "200") {
		t.Fatalf("BYE want 200, got: %.200s", resp)
	}
	return resp
}

func closePeer(t *testing.T, conn net.Conn, mode string) {
	t.Helper()
	switch mode {
	case "close":
		if err := conn.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	case "reset":
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("reset close: %v", err)
		}
	default:
		t.Fatalf("unknown close mode %s", mode)
	}
}

func startTCPPlay(t *testing.T, sipPort int, hub *FrameHub, callID string) (net.Listener, net.Conn, context.CancelFunc) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	resp := sendInviteWithCallID(t, sipPort, callID, playSDPTCP(ln.Addr().(*net.TCPAddr).Port))
	if !strings.Contains(resp, "200 OK") {
		t.Fatalf("INVITE want 200 OK, got: %.200s", resp)
	}
	conn := acceptTCP(t, ln, 3*time.Second)
	waitSubscribed(t, hub, 3*time.Second)
	feedCtx, stopFeed := context.WithCancel(context.Background())
	t.Cleanup(stopFeed)
	feedKeyframes(feedCtx, hub, 10*time.Millisecond)
	readFramedRTP(t, conn, 3*time.Second)
	return ln, conn, stopFeed
}

func testTCPPeerDrop(t *testing.T, mode string) {
	logs := hookLogs(t)
	_, sipPort, hub := startTestServer(t)
	_, conn, stopFeed := startTCPPlay(t, sipPort, hub, "tcp-fail-"+mode+"-"+fmtShort())
	closePeer(t, conn, mode)
	waitSubscribers(t, hub, 0, 3*time.Second)

	beforeFrame := logs.get("gb28181: failed to send RTP frame")
	beforePerm := logs.get("gb28181: media TCP send failed permanently, stopping media session")
	if beforePerm < 1 {
		t.Fatalf("expected a permanent-stop log, got %d (per-frame=%d)", beforePerm, beforeFrame)
	}
	time.Sleep(200 * time.Millisecond)
	afterFrame := logs.get("gb28181: failed to send RTP frame")
	afterPerm := logs.get("gb28181: media TCP send failed permanently, stopping media session")
	if afterFrame-beforeFrame > 2 {
		t.Fatalf("per-frame warnings kept growing after stop: %d -> %d", beforeFrame, afterFrame)
	}
	if afterPerm != beforePerm {
		t.Fatalf("permanent-stop log repeated: %d -> %d", beforePerm, afterPerm)
	}
	if hub.SubscriberCount() != 0 {
		t.Fatalf("session still subscribed after TCP death: %d", hub.SubscriberCount())
	}
	stopFeed()
}

func TestInviteTCP_PeerCloseStopsMediaNoFlood(t *testing.T) {
	testTCPPeerDrop(t, "close")
}

func TestInviteTCP_PeerResetStopsMediaNoFlood(t *testing.T) {
	testTCPPeerDrop(t, "reset")
}

func TestInviteTCP_PeerCloseThenBYEIdempotent(t *testing.T) {
	hookLogs(t)
	_, sipPort, hub := startTestServer(t)
	callID := "tcp-bye-race-" + fmtShort()
	_, conn, stopFeed := startTCPPlay(t, sipPort, hub, callID)
	defer stopFeed()
	closePeer(t, conn, "close")
	sendByeOK(t, sipPort, callID)
	waitSubscribers(t, hub, 0, 3*time.Second)
	sendByeOK(t, sipPort, callID)
	if hub.SubscriberCount() != 0 {
		t.Fatalf("subscribers after second BYE: %d", hub.SubscriberCount())
	}
}

func TestInviteTCP_PeerCloseThenReInviteRecovers(t *testing.T) {
	logs := hookLogs(t)
	_, sipPort, hub := startTestServer(t)
	_, conn, stopFeed := startTCPPlay(t, sipPort, hub, "tcp-reinvite-1-"+fmtShort())
	closePeer(t, conn, "reset")
	waitSubscribers(t, hub, 0, 3*time.Second)
	stopFeed()
	if logs.get("gb28181: media TCP send failed permanently, stopping media session") < 1 {
		t.Fatal("expected permanent stop before re-INVITE")
	}

	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen2: %v", err)
	}
	defer ln2.Close()
	resp := sendInviteWithCallID(t, sipPort, "tcp-reinvite-2-"+fmtShort(), playSDPTCP(ln2.Addr().(*net.TCPAddr).Port))
	if !strings.Contains(resp, "200 OK") {
		t.Fatalf("re-INVITE want 200 OK, got: %.200s", resp)
	}
	conn2 := acceptTCP(t, ln2, 3*time.Second)
	defer conn2.Close()
	waitSubscribed(t, hub, 3*time.Second)
	feedCtx, stop := context.WithCancel(context.Background())
	defer stop()
	feedKeyframes(feedCtx, hub, 10*time.Millisecond)
	readFramedRTP(t, conn2, 3*time.Second)
}

func TestInviteUDP_PeerSilenceDoesNotStopSession(t *testing.T) {
	logs := hookLogs(t)
	_, sipPort, hub := startTestServer(t)

	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	callID := "udp-keep-" + fmtShort()
	resp := sendInviteWithCallID(t, sipPort, callID, playSDPUDP(pc.LocalAddr().(*net.UDPAddr).Port))
	if !strings.Contains(resp, "200 OK") {
		t.Fatalf("UDP INVITE want 200 OK, got: %.200s", resp)
	}
	waitSubscribed(t, hub, 3*time.Second)
	feedCtx, stopFeed := context.WithCancel(context.Background())
	defer stopFeed()
	feedKeyframes(feedCtx, hub, 10*time.Millisecond)

	pc.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	if _, _, err := pc.ReadFromUDP(buf); err != nil {
		t.Fatalf("first UDP RTP: %v", err)
	}
	_ = pc.Close()
	time.Sleep(200 * time.Millisecond)
	if hub.SubscriberCount() == 0 {
		t.Fatal("UDP session stopped after peer socket close; UDP should keep running until BYE")
	}
	if logs.get("gb28181: media TCP send failed permanently, stopping media session") != 0 {
		t.Fatal("UDP path used TCP permanent-stop")
	}
	sendByeOK(t, sipPort, callID)
	waitSubscribers(t, hub, 0, 3*time.Second)
}