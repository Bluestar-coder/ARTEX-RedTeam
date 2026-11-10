package recon

import (
	"net/http"
	"testing"
)

func TestMurmur3KnownVector(t *testing.T) {
	// mmh3.hash("foo") == -156908512 (Python mmh3 / x86 32-bit variant).
	got := itoaSigned(int32(murmur3_32([]byte("foo"), 0)))
	if got != "-156908512" {
		t.Fatalf("mmh3(foo)=%s", got)
	}
}

func TestDetectTechnologies(t *testing.T) {
	rec := detectTechnologies(nil, []byte(`<html><title>t</title><link href="/_next/static/x"><div data-reactroot></div></html>`))
	joined := ""
	for _, x := range rec {
		joined += x + ","
	}
	if joined != "Next.js,React," {
		t.Fatalf("tech=%v", rec)
	}
}

func TestFaviconHashIsStable(t *testing.T) {
	a := faviconMMH3([]byte{0x00, 0x01, 0x02, 0x03})
	b := faviconMMH3([]byte{0x00, 0x01, 0x02, 0x03})
	if a != b || a == "" {
		t.Fatalf("favicon hash unstable: %q %q", a, b)
	}
}

func TestDetectTechnologiesAngularAndSpring(t *testing.T) {
	h := http.Header{}
	h.Set("X-Application-Context", "application:prod")
	got := detectTechnologies(h, []byte(`<html><app-root></app-root></html>`))
	if !containsTech(got, "Angular") {
		t.Fatalf("Angular not detected: %v", got)
	}
	if !containsTech(got, "Spring") {
		t.Fatalf("Spring not detected: %v", got)
	}
}

func containsTech(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
