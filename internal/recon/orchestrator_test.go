package recon

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeProvider struct {
	name string
	recs []Record
	err  error
}

func (f fakeProvider) Name() string { return f.name }
func (f fakeProvider) Run(context.Context, Request) ([]Record, error) {
	return f.recs, f.err
}

func TestRunScopeDedupeAndDegrade(t *testing.T) {
	now := time.Now().UTC()
	reg := NewRegistry()
	reg.Register(fakeProvider{name: "a", recs: []Record{
		{Type: TypeSubdomain, Value: "www.example.com", Source: "a", ObservedAt: now, Extra: map[string]any{"x": 1}},
		{Type: TypeSubdomain, Value: "www.example.com", Source: "a", ObservedAt: now.Add(time.Second)},
		{Type: TypeSubdomain, Value: "evil.other.com", Source: "a", ObservedAt: now},
	}}, true)
	reg.Register(fakeProvider{name: "b", err: errors.New("quota exceeded")}, true)
	reg.Register(fakeProvider{name: "c", recs: []Record{
		{Type: TypeRootDomain, Value: "example.com", Source: "c", ObservedAt: now},
	}}, true)
	scope, err := NewScope("example.com", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := Run(context.Background(), reg, "example.com", scope, Config{})
	if len(res.Providers) != 3 {
		t.Fatalf("provider stats=%d", len(res.Providers))
	}
	var bStat Stat
	for _, s := range res.Providers {
		if s.Name == "b" {
			bStat = s
		}
	}
	if bStat.Error == "" {
		t.Fatal("failing provider should be reported but not fatal")
	}
	if len(res.Rejected) != 1 || res.Rejected[0].Value != "evil.other.com" {
		t.Fatalf("rejections=%+v", res.Rejected)
	}
	// www (merged, one row) + root domain
	if len(res.Records) != 2 {
		t.Fatalf("records=%+v", res.Records)
	}
	var www Record
	for _, r := range res.Records {
		if r.Value == "www.example.com" {
			www = r
		}
	}
	if www.Source != "a" || www.Extra["sources"] == nil {
		t.Fatalf("merged record=%+v", www)
	}
}
