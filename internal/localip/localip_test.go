package localip

import (
	"context"
	"testing"
	"time"
)

func TestDetectLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ip, err := Detect(ctx, "127.0.0.1", 5060)
	if err != nil {
		t.Fatal(err)
	}
	if ip != "127.0.0.1" {
		t.Fatalf("Detect(127.0.0.1:5060) = %q, want 127.0.0.1", ip)
	}
}

func TestDetectRejectsEmpty(t *testing.T) {
	if _, err := Detect(context.Background(), "", 5060); err == nil {
		t.Fatal("expected error for empty host")
	}
	if _, err := Detect(context.Background(), "127.0.0.1", 0); err == nil {
		t.Fatal("expected error for invalid port")
	}
}
