package runner

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mickeyzzc/gb28181-go/device"

	"gb28181-device-ui/internal/appconfig"
	"gb28181-device-ui/internal/logview"
)

// startUIDevice boots a device the same way the desktop UI does (via Runner),
// but in library test-mode so REGISTER is skipped. Returns the bound SIP port
// and a UDP socket that acts as the platform.
func startUIDevice(t *testing.T) (port int, plat *net.UDPConn, stop func()) {
	t.Helper()

	l, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port = l.LocalAddr().(*net.UDPAddr).Port
	_ = l.Close()

	logs := logview.New(nil)
	r := New(logs)
	cfg := appconfig.Defaults()
	cfg.Password = "test-password"
	cfg.LocalSIPPort = port
	cfg.PlatformSIPAddress = "127.0.0.1"
	cfg.PlatformSIPPort = 15060
	cfg.VideoBackend = "synthetic"

	if err := r.Start(cfg, nil); err != nil {
		t.Fatalf("runner.Start: %v", err)
	}
	// Switch the live server into test-mode after Start wired SIP — the
	// library's SetTestMode is intended before Start, so instead wait for
	// the bind and drive MESSAGE/INVITE directly. Registration may fail
	// against 127.0.0.1:15060; listen-only mode still answers queries.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if srv := r.Server(); srv != nil {
			if p, err := srv.SIPPort(); err == nil && p > 0 {
				port = p
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	plat, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		r.Stop()
		t.Fatalf("platform listen: %v", err)
	}

	stop = func() {
		_ = plat.Close()
		r.Stop()
	}
	return port, plat, stop
}

func writeSIP(t *testing.T, plat *net.UDPConn, port int, wire string) {
	t.Helper()
	dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	if _, err := plat.WriteToUDP([]byte(wire), dst); err != nil {
		t.Fatalf("write SIP: %v", err)
	}
}

func readSIP(t *testing.T, plat *net.UDPConn, wait time.Duration) (string, bool) {
	t.Helper()
	_ = plat.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 65535)
	n, _, err := plat.ReadFromUDP(buf)
	if err != nil {
		return "", false
	}
	return string(buf[:n]), true
}

func TestRunner_CatalogQueryAnswersMANSCDP(t *testing.T) {
	port, plat, stop := startUIDevice(t)
	defer stop()

	cfg := appconfig.Defaults()
	body := fmt.Sprintf(`<?xml version="1.0"?><Query><CmdType>Catalog</CmdType><SN>77</SN><DeviceID>%s</DeviceID></Query>`, cfg.DeviceID)
	platPort := plat.LocalAddr().(*net.UDPAddr).Port
	wire := fmt.Sprintf("MESSAGE sip:%s@%s SIP/2.0\r\n"+
		"Via: SIP/2.0/UDP 127.0.0.1:%d;rport;branch=z9hG4bKcat-ui\r\n"+
		"From: <sip:35080000002000000001@%s>;tag=p\r\n"+
		"To: <sip:%s@%s>\r\n"+
		"Call-ID: cat-ui@plat\r\nCSeq: 1 MESSAGE\r\n"+
		"Content-Type: Application/MANSCDP+xml\r\nContent-Length: %d\r\n\r\n%s",
		cfg.DeviceID, cfg.SIPDomain, platPort, cfg.SIPDomain, cfg.DeviceID, cfg.SIPDomain, len(body), body)
	writeSIP(t, plat, port, wire)

	saw200, sawCatalog := false, false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, ok := readSIP(t, plat, 400*time.Millisecond)
		if !ok {
			continue
		}
		if strings.HasPrefix(s, "SIP/2.0 200") {
			saw200 = true
		}
		if strings.Contains(s, "MESSAGE") && strings.Contains(s, "<CmdType>Catalog</CmdType>") &&
			strings.Contains(s, "SumNum") && strings.Contains(s, cfg.ChannelID) {
			sawCatalog = true
			if !strings.Contains(s, "<SN>77</SN>") {
				t.Fatalf("Catalog Response missing element SN=77:\n%s", s)
			}
		}
		if saw200 && sawCatalog {
			break
		}
	}
	if !saw200 {
		t.Fatal("Catalog Query must get SIP 200 OK")
	}
	if !sawCatalog {
		t.Fatal("Catalog Query must get MANSCDP Catalog Response MESSAGE with channel list")
	}
}

func TestRunner_DeviceInfoAndStatusAnswers(t *testing.T) {
	port, plat, stop := startUIDevice(t)
	defer stop()
	cfg := appconfig.Defaults()
	platPort := plat.LocalAddr().(*net.UDPAddr).Port

	for _, tc := range []struct {
		cmd  string
		want string
	}{
		{"DeviceInfo", "DeviceName"},
		{"DeviceStatus", "Online"},
	} {
		body := fmt.Sprintf(`<?xml version="1.0"?><Query><CmdType>%s</CmdType><SN>9</SN><DeviceID>%s</DeviceID></Query>`, tc.cmd, cfg.DeviceID)
		wire := fmt.Sprintf("MESSAGE sip:%s@%s SIP/2.0\r\n"+
			"Via: SIP/2.0/UDP 127.0.0.1:%d;rport;branch=z9hG4bK%s\r\n"+
			"From: <sip:35080000002000000001@%s>;tag=p\r\n"+
			"To: <sip:%s@%s>\r\n"+
			"Call-ID: %s-ui@plat\r\nCSeq: 1 MESSAGE\r\n"+
			"Content-Type: Application/MANSCDP+xml\r\nContent-Length: %d\r\n\r\n%s",
			cfg.DeviceID, cfg.SIPDomain, platPort, tc.cmd, cfg.SIPDomain, cfg.DeviceID, cfg.SIPDomain, tc.cmd, len(body), body)
		writeSIP(t, plat, port, wire)

		saw := false
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			s, ok := readSIP(t, plat, 300*time.Millisecond)
			if !ok {
				continue
			}
			if strings.Contains(s, "MESSAGE") && strings.Contains(s, tc.cmd) && strings.Contains(s, tc.want) {
				saw = true
				break
			}
		}
		if !saw {
			t.Fatalf("%s Query must get Response containing %q", tc.cmd, tc.want)
		}
	}
}

func TestRunner_InviteAckByeWithSDP(t *testing.T) {
	port, plat, stop := startUIDevice(t)
	defer stop()
	cfg := appconfig.Defaults()
	platPort := plat.LocalAddr().(*net.UDPAddr).Port

	sdp := "v=0\r\no=35080000002000000001 0 0 IN IP4 127.0.0.1\r\ns=Play\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=video 20000 RTP/AVP 96\r\na=rtpmap:96 PS/90000\r\ny=0100000001\r\n"
	inv := fmt.Sprintf("INVITE sip:%s@%s SIP/2.0\r\n"+
		"Via: SIP/2.0/UDP 127.0.0.1:%d;rport;branch=z9hG4bKinv-ui\r\n"+
		"From: <sip:35080000002000000001@%s>;tag=i\r\n"+
		"To: <sip:%s@%s>\r\n"+
		"Call-ID: inv-ui@plat\r\nCSeq: 1 INVITE\r\n"+
		"Contact: <sip:35080000002000000001@127.0.0.1:%d>\r\n"+
		"Subject: %s:0100000001,35080000002000000001:0\r\n"+
		"Content-Type: application/sdp\r\nContent-Length: %d\r\n\r\n%s",
		cfg.ChannelID, cfg.SIPDomain, platPort, cfg.SIPDomain, cfg.ChannelID, cfg.SIPDomain,
		platPort, cfg.ChannelID, len(sdp), sdp)
	writeSIP(t, plat, port, inv)

	var invite200 string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, ok := readSIP(t, plat, 400*time.Millisecond)
		if !ok {
			continue
		}
		if strings.HasPrefix(s, "SIP/2.0 200") && strings.Contains(s, "application/sdp") {
			invite200 = s
			break
		}
	}
	if invite200 == "" {
		t.Fatal("INVITE must get 200 OK with SDP")
	}
	if !strings.Contains(invite200, "m=video") || !strings.Contains(invite200, "y=") {
		t.Fatalf("INVITE 200 SDP incomplete:\n%s", invite200)
	}
	if !strings.Contains(strings.ToLower(invite200), "contact:") {
		t.Fatalf("INVITE 200 must carry Contact for ACK/BYE routing:\n%s", invite200)
	}

	// ACK — device needs no body answer; ensure it does not crash.
	ack := fmt.Sprintf("ACK sip:%s@%s SIP/2.0\r\n"+
		"Via: SIP/2.0/UDP 127.0.0.1:%d;rport;branch=z9hG4bKack-ui\r\n"+
		"From: <sip:35080000002000000001@%s>;tag=i\r\n"+
		"To: <sip:%s@%s>;tag=x\r\n"+
		"Call-ID: inv-ui@plat\r\nCSeq: 1 ACK\r\nContent-Length: 0\r\n\r\n",
		cfg.ChannelID, cfg.SIPDomain, platPort, cfg.SIPDomain, cfg.ChannelID, cfg.SIPDomain)
	writeSIP(t, plat, port, ack)

	bye := fmt.Sprintf("BYE sip:%s@%s SIP/2.0\r\n"+
		"Via: SIP/2.0/UDP 127.0.0.1:%d;rport;branch=z9hG4bKbye-ui\r\n"+
		"From: <sip:35080000002000000001@%s>;tag=i\r\n"+
		"To: <sip:%s@%s>;tag=x\r\n"+
		"Call-ID: inv-ui@plat\r\nCSeq: 2 BYE\r\nContent-Length: 0\r\n\r\n",
		cfg.ChannelID, cfg.SIPDomain, platPort, cfg.SIPDomain, cfg.ChannelID, cfg.SIPDomain)
	writeSIP(t, plat, port, bye)

	sawBye200 := false
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, ok := readSIP(t, plat, 300*time.Millisecond)
		if !ok {
			continue
		}
		if strings.HasPrefix(s, "SIP/2.0 200") && strings.Contains(s, "CSeq: 2 BYE") {
			sawBye200 = true
			break
		}
	}
	if !sawBye200 {
		t.Fatal("BYE must get SIP 200 OK")
	}
}

func TestRunner_InviteRejectsBadSDPWith488(t *testing.T) {
	port, plat, stop := startUIDevice(t)
	defer stop()
	cfg := appconfig.Defaults()
	platPort := plat.LocalAddr().(*net.UDPAddr).Port

	// No y= and no usable Subject SSRC → must 488, not silent drop.
	sdp := "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=Play\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=video 20000 RTP/AVP 96\r\n"
	inv := fmt.Sprintf("INVITE sip:%s@%s SIP/2.0\r\n"+
		"Via: SIP/2.0/UDP 127.0.0.1:%d;rport;branch=z9hG4bKbad-ui\r\n"+
		"From: <sip:35080000002000000001@%s>;tag=b\r\n"+
		"To: <sip:%s@%s>\r\n"+
		"Call-ID: bad-ui@plat\r\nCSeq: 1 INVITE\r\n"+
		"Content-Type: application/sdp\r\nContent-Length: %d\r\n\r\n%s",
		cfg.ChannelID, cfg.SIPDomain, platPort, cfg.SIPDomain, cfg.ChannelID, cfg.SIPDomain, len(sdp), sdp)
	writeSIP(t, plat, port, inv)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, ok := readSIP(t, plat, 300*time.Millisecond)
		if !ok {
			continue
		}
		if strings.HasPrefix(s, "SIP/2.0 488") {
			return
		}
	}
	t.Fatal("INVITE with unusable SDP must be rejected with 488")
}

// Ensure the device package builders the UI depends on stay importable.
var _ = device.Config{}


// TestRunner_CatalogQuery_GatewayWireFormat sends a Catalog Query using the
// same child-element GB2312 body and Content-Type as sdSecureGateway, then
// requires SIP 200 plus an element-form Catalog Response that the gateway's
// strstr("<CmdType>Catalog</CmdType>") check would accept. Uses ephemeral
// ports only (never 5060/15091).
func TestRunner_CatalogQuery_GatewayWireFormat(t *testing.T) {
	port, plat, stop := startUIDevice(t)
	defer stop()

	cfg := appconfig.Defaults()
	body := "<?xml version=\"1.0\" encoding=\"GB2312\"?>\r\n" +
		"<Query>\r\n" +
		"<CmdType>Catalog</CmdType>\r\n" +
		"<SN>6</SN>\r\n" +
		"<DeviceID>" + cfg.DeviceID + "</DeviceID>\r\n" +
		"</Query>\r\n"
	platPort := plat.LocalAddr().(*net.UDPAddr).Port
	if platPort == 5060 || port == 15091 || platPort == 15091 || port == 5060 {
		t.Fatalf("refusing to use reserved field ports device=%d platform=%d", port, platPort)
	}
	wire := fmt.Sprintf("MESSAGE sip:%s@127.0.0.1:%d SIP/2.0\r\n"+
		"Via: SIP/2.0/UDP 127.0.0.1:%d;rport;branch=z9hG4bKgw-cat\r\n"+
		"From: <sip:35080000002000000001@127.0.0.1:%d>;tag=p\r\n"+
		"To: <sip:%s@127.0.0.1:%d>\r\n"+
		"Call-ID: 6gwcat@plat\r\nCSeq: 6 MESSAGE\r\n"+
		"Max-Forwards: 70\r\n"+
		"Content-Type: Application/MANSCDP+xml;charset=GB2312\r\n"+
		"Content-Length: %d\r\n\r\n%s",
		cfg.DeviceID, port, platPort, platPort, cfg.DeviceID, port, len(body), body)
	writeSIP(t, plat, port, wire)

	saw200, sawCatalog := false, false
	var last string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, ok := readSIP(t, plat, 400*time.Millisecond)
		if !ok {
			continue
		}
		last = s
		if strings.HasPrefix(s, "SIP/2.0 200") {
			saw200 = true
		}
		if strings.Contains(s, "MESSAGE") &&
			strings.Contains(s, "<CmdType>Catalog</CmdType>") &&
			strings.Contains(s, "<Response>") &&
			strings.Contains(s, "<SN>6</SN>") &&
			strings.Contains(s, cfg.ChannelID) {
			sawCatalog = true
		}
		if saw200 && sawCatalog {
			break
		}
	}
	if !saw200 {
		t.Fatalf("gateway-style Catalog Query must get SIP 200 OK; last=\n%s", last)
	}
	if !sawCatalog {
		t.Fatalf("gateway-style Catalog Query must get element-form Catalog Response; last=\n%s", last)
	}
}
