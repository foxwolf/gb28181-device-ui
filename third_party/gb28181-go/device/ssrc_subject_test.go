package device

import "testing"

func TestSSRCFromSubject(t *testing.T) {
	msg := SipMessage{Headers: map[string]string{
		"Subject": "35020000001320000005:0100000001,35080000002000000001:0",
	}}
	ssrc, ok := ssrcFromSubject(msg)
	if !ok {
		t.Fatal("expected SSRC from Subject")
	}
	if ssrc != 100000001 {
		t.Fatalf("ssrc = %d, want 100000001", ssrc)
	}

	empty := SipMessage{Headers: map[string]string{}}
	if _, ok := ssrcFromSubject(empty); ok {
		t.Fatal("empty Subject must not yield SSRC")
	}
}
