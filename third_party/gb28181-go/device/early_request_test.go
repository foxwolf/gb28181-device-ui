package device

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// TestEarlyCatalogDuringRegisterWait reproduces the field failure mode:
// platform sends Catalog Query while the device is still blocked in the
// initial REGISTER response wait. The Catalog must be answered (SIP 200 +
// MANSCDP Catalog Response), not swallowed as a fake REGISTER response.
func TestEarlyCatalogDuringRegisterWait(t *testing.T) {
	plat, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plat.Close() })
	platPort := plat.LocalAddr().(*net.UDPAddr).Port

	hub := NewFrameHub()
	cfg := Config{
		Enabled:               true,
		DeviceID:              "34020000001320000001",
		ChannelID:             "34020000001320000002",
		SIPDomain:             "3402000000",
		Password:              "12345678",
		LocalSIPPort:          0,
		PlatformSIPAddress:    "127.0.0.1",
		PlatformSIPPort:       platPort,
		RegisterIntervalSecs:  60,
		HeartbeatIntervalSecs: 60,
		HeartbeatTimeoutCount: 3,
	}
	srv := New(cfg, DeviceInfo{Name: "Cam", Manufacturer: "Unknown", Model: "Unknown"}, hub)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		srv.Stop()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
		}
	})

	port := waitSIPPort(t, srv)

	// First datagram from the device should be REGISTER. Answer nothing yet —
	// keep the device inside socketResponseSource — then send Catalog.
	_ = plat.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 65535)
	n, _, err := plat.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("waiting for REGISTER: %v", err)
	}
	reg, err := Parse(buf[:n])
	if err != nil || reg.Method != "REGISTER" {
		t.Fatalf("expected REGISTER, got method=%q err=%v body=%q", reg.Method, err, string(buf[:n]))
	}

	body := `<?xml version="1.0"?><Query><CmdType>Catalog</CmdType><SN>42</SN><DeviceID>34020000001320000001</DeviceID></Query>`
	cat := SipMessage{
		Method:      "MESSAGE",
		RequestURI:  "sip:34020000001320000001@3402000000",
		From:        "<sip:34020000002000000001@3402000000>;tag=p",
		To:          "<sip:34020000001320000001@3402000000>",
		CallID:      "early-cat@plat",
		CSeq:        "1 MESSAGE",
		Via:         "SIP/2.0/UDP 127.0.0.1;rport;branch=z9hG4bKearly",
		ContentType: "Application/MANSCDP+xml",
		Body:        body,
		Headers:     map[string]string{},
	}
	dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	if _, err := plat.WriteToUDP(cat.Serialize(), dst); err != nil {
		t.Fatal(err)
	}

	saw200, sawCatalog := false, false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = plat.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
		n, _, err := plat.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		s := string(buf[:n])
		if strings.HasPrefix(s, "SIP/2.0 200") && strings.Contains(s, "CSeq: 1 MESSAGE") {
			saw200 = true
		}
		if strings.Contains(s, "MESSAGE") && strings.Contains(s, "Catalog") &&
			strings.Contains(s, "SumNum") && strings.Contains(s, "34020000001320000002") {
			sawCatalog = true
		}
		if saw200 && sawCatalog {
			break
		}
	}
	if !saw200 {
		t.Fatal("early Catalog Query must receive SIP 200 OK during REGISTER wait")
	}
	if !sawCatalog {
		t.Fatal("early Catalog Query must receive MANSCDP Catalog Response during REGISTER wait")
	}
}
