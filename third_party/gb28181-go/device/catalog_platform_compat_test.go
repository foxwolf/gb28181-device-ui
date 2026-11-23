package device

import (
	"strings"
	"testing"
)

// Matches sdSecureGateway serverHandleMessage: a MESSAGE is a catalog
// answer only when the body contains these exact child-element tags.
func gatewayRecognizesCatalog(body string) bool {
	return strings.Contains(body, "<CmdType>Catalog</CmdType>") && strings.Contains(body, "<Response>")
}

// Matches sipKeepaliveParse: requires a plain <Notify> start tag and
// child-element CmdType/DeviceID/Status.
func gatewayRecognizesKeepalive(body string) bool {
	return strings.Contains(body, "<Notify>") &&
		strings.Contains(body, "<CmdType>Keepalive</CmdType>") &&
		strings.Contains(body, "<DeviceID>") &&
		strings.Contains(body, "<Status>")
}

func TestCatalogResponse_ElementFormRecognizedByFieldGateway(t *testing.T) {
	msg := BuildCatalogResponseMessage("6", "35080000001320000011", []ChannelItem{{
		DeviceID: "35020000001320000005",
		Name:     "yangm",
		Status:   "ON",
		ParentID: "35080000001320000011",
	}})
	if !gatewayRecognizesCatalog(msg.Body) {
		t.Fatalf("field gateway would ignore this Catalog body:\n%s", msg.Body)
	}
	if !strings.Contains(msg.Body, "<SN>6</SN>") {
		t.Fatalf("missing element SN:\n%s", msg.Body)
	}
	if strings.Contains(msg.Body, `CmdType="Catalog"`) {
		t.Fatalf("attribute-form CmdType would not be recognized:\n%s", msg.Body)
	}
}

func TestKeepalive_ElementFormRecognizedByFieldGateway(t *testing.T) {
	msg := BuildKeepaliveMessage("1", "35080000001320000011", "OK")
	if !gatewayRecognizesKeepalive(msg.Body) {
		t.Fatalf("field gateway keepalive parser would reject:\n%s", msg.Body)
	}
	if strings.Contains(msg.Body, `CmdType="Keepalive"`) {
		t.Fatalf("attribute-form Keepalive would not refresh heartbeat:\n%s", msg.Body)
	}
}