package agent

import (
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/jsintel"
)

func TestMergeStaticInterfacesByPathAcrossHosts(t *testing.T) {
	in := []jsintel.Interface{
		{Host: "www.juice.localhost", Method: "unknown", Path: "/api/Users", Confidence: jsintel.ConfidenceLow},
		{Host: "api.juice.localhost", Method: "unknown", Path: "/api/Users", Confidence: jsintel.ConfidenceLow, Params: []string{"id"}},
		{Host: "juice.localhost", Method: "GET", Path: "/rest/products/search", Confidence: jsintel.ConfidenceHigh},
	}
	got := mergeStaticInterfacesByPath(in, "juice.localhost")
	if len(got) != 2 {
		t.Fatalf("merged=%d, want 2: %+v", len(got), got)
	}
	var users *mergedStaticInterface
	for i := range got {
		if got[i].itf.Path == "/api/Users" {
			users = &got[i]
		}
	}
	if users == nil {
		t.Fatal("missing /api/Users")
	}
	if users.itf.Host != "juice.localhost" {
		t.Fatalf("representative host=%q, want supplied domain", users.itf.Host)
	}
	if len(users.hosts) != 2 || !containsString(users.hosts, "www.juice.localhost") || !containsString(users.hosts, "api.juice.localhost") {
		t.Fatalf("hosts=%v, want both aliases", users.hosts)
	}
	if len(users.itf.Params) != 1 || users.itf.Params[0] != "id" {
		t.Fatalf("params not merged: %v", users.itf.Params)
	}
}
