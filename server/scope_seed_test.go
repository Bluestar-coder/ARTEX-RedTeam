package server

import (
	"strconv"
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
)

func TestTaskSeedDerivesNetworkScope(t *testing.T) {
	m, err := NewManager(t.TempDir(), "", "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s := &Server{m: m}

	ipTask, err := m.CreateTask("ip target", "scan", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.seed(ipTask, "http://127.0.0.1:3000/")
	ipID, _ := strconv.ParseInt(ipTask.ID, 10, 64)
	scopes, err := m.Assets().ListTaskScope(ipID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasScopeKind(scopes, "ip", "127.0.0.1/32") {
		t.Fatalf("IP seed did not persist IP scope: %+v", scopes)
	}

	domainTask, err := m.CreateTask("domain target", "scan", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.seed(domainTask, "http://www.example.com/")
	domainID, _ := strconv.ParseInt(domainTask.ID, 10, 64)
	scopes, err = m.Assets().ListTaskScope(domainID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasScopeKind(scopes, "root_domain", "example.com") {
		t.Fatalf("domain seed missing root_domain scope: %+v", scopes)
	}
	if !hasScopeKind(scopes, "subdomain", "www.example.com") {
		t.Fatalf("domain seed missing seed-host subdomain scope: %+v", scopes)
	}
}

func hasScopeKind(scopes []db.TaskScope, kind, value string) bool {
	for _, sc := range scopes {
		switch kind {
		case "ip":
			if sc.Kind == kind && sc.Net == value {
				return true
			}
		default:
			if sc.Kind == kind && sc.Domain == value {
				return true
			}
		}
	}
	return false
}

func TestTaskSeedCreatesScopePerTarget(t *testing.T) {
	m, err := NewManager(t.TempDir(), "", "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s := &Server{m: m}
	task, err := m.CreateTask("multi scope", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.seed(task, "targets http://one.example.com:3000/ http://two.example.org:8081/ http://10.9.8.7:8082/")
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	scopes, err := m.Assets().ListTaskScope(id)
	if err != nil {
		t.Fatal(err)
	}
	if !hasScopeKind(scopes, "root_domain", "example.com") ||
		!hasScopeKind(scopes, "subdomain", "one.example.com") ||
		!hasScopeKind(scopes, "root_domain", "example.org") ||
		!hasScopeKind(scopes, "subdomain", "two.example.org") ||
		!hasScopeKind(scopes, "ip", "10.9.8.7/32") {
		t.Fatalf("multi-target scopes missing: %+v", scopes)
	}
}
