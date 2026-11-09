package recon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeCT struct {
	name string
	recs []CTName
	err  error
	hits int
}

func (f *fakeCT) Name() string { return f.name }
func (f *fakeCT) Lookup(context.Context, string) ([]CTName, error) {
	f.hits++
	return f.recs, f.err
}

func TestCTBackendFallbackOrder(t *testing.T) {
	fail := &fakeCT{name: "first", err: context.DeadlineExceeded}
	ok := &fakeCT{name: "second", recs: []CTName{{Name: "www.example.com"}}}
	p := &ActiveProvider{ctBackends: []CTBackend{fail, ok}}
	names, backend, err := p.enumerateCT(context.Background(), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if backend != "second" || len(names) != 1 || names[0].Name != "www.example.com" {
		t.Fatalf("backend=%q names=%+v", backend, names)
	}
	if fail.hits != 1 || ok.hits != 1 {
		t.Fatalf("hits first=%d second=%d", fail.hits, ok.hits)
	}

	allFail := &ActiveProvider{ctBackends: []CTBackend{fail, &fakeCT{name: "third", err: context.DeadlineExceeded}}}
	if _, _, err := allFail.enumerateCT(context.Background(), "example.com"); err == nil {
		t.Fatal("all-failed should return an error so the caller falls back to dictionary")
	}
}

func TestCTBackendParsers(t *testing.T) {
	crtSh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name_value": "www.example.com\n*.api.example.com", "not_before": "2024-01-02T03:04:05Z", "id": 42},
		})
	}))
	defer crtSh.Close()
	sh := &crtShBackend{baseURL: crtSh.URL, client: crtSh.Client()}
	got, err := sh.Lookup(context.Background(), "example.com")
	if err != nil || len(got) != 2 || got[0].FirstSeen.IsZero() {
		t.Fatalf("crt.sh=%+v err=%v", got, err)
	}

	crtNameText := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("www.example.com 2024-01-02\nmail.example.com\n"))
	}))
	defer crtNameText.Close()
	cn := &crtNameBackend{baseURL: crtNameText.URL, client: crtNameText.Client()}
	got, err = cn.Lookup(context.Background(), "example.com")
	if err != nil || len(got) != 2 {
		t.Fatalf("crt.name text=%+v err=%v", got, err)
	}

	crtNameJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"domain":"api.example.com","first_seen":"2024-02-03T04:05:06Z"}]}`))
	}))
	defer crtNameJSON.Close()
	cn2 := &crtNameBackend{baseURL: crtNameJSON.URL, client: crtNameJSON.Client()}
	got, err = cn2.Lookup(context.Background(), "example.com")
	if err != nil || len(got) != 1 || got[0].Name != "api.example.com" || got[0].FirstSeen.IsZero() {
		t.Fatalf("crt.name json=%+v err=%v", got, err)
	}

	var sawToken bool
	cs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawToken = r.Header.Get("Authorization") == "Bearer tok"
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "iss_1", "not_before": "2024-03-04T05:06:07Z", "dns_names": []string{"www.example.com", "example.com"}},
		})
	}))
	defer cs.Close()
	spot := &certSpotterBackend{baseURL: cs.URL, token: "tok", client: cs.Client()}
	got, err = spot.Lookup(context.Background(), "example.com")
	if err != nil || len(got) != 2 || !sawToken || got[0].FirstSeen.IsZero() {
		t.Fatalf("certspotter=%+v err=%v token=%v", got, err, sawToken)
	}
}
