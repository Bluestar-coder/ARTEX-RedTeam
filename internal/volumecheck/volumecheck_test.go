package volumecheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildHintKeepsOnlyOtherProjectsPgdata(t *testing.T) {
	containers := []dockerContainer{
		{
			ID:     "aaaaaaaaaaaaaaaa",
			Names:  []string{"/new-artex-redteam-1"},
			Labels: map[string]string{labelProject: "new"},
			Mounts: []dockerMount{{Type: "bind", Source: "/work/new/data", Destination: "/app/data-redteam"}},
		},
		{
			ID:     "bbbbbbbbbbbbbbbb",
			Names:  []string{"/old-artex-redteam-1"},
			Labels: map[string]string{labelProject: "old", labelWorkingDir: "/work/old"},
			Mounts: []dockerMount{{Type: "bind", Source: "/work/old/data", Destination: "/app/data-redteam"}},
		},
	}
	volumes := []dockerVolume{
		{Name: "new_pgdata", Labels: map[string]string{labelProject: "new", labelVolume: "pgdata"}},
		{Name: "old_pgdata", Labels: map[string]string{labelProject: "old", labelVolume: "pgdata"}},
		{Name: "dbdata", Labels: map[string]string{labelProject: "other", labelVolume: "dbdata"}},
	}

	h := BuildHint("new", containers, volumes)
	if h.CurrentProject != "new" {
		t.Fatalf("current = %q, want new", h.CurrentProject)
	}
	if len(h.OldProjects) != 1 {
		t.Fatalf("old projects = %+v, want exactly old", h.OldProjects)
	}
	got := h.OldProjects[0]
	if got.Name != "old" || got.Volume != "old_pgdata" || got.DataDir != "/work/old/data" {
		t.Fatalf("old project = %+v", got)
	}
}

func TestBuildHintIgnoresProjectsWithoutPgdataVolume(t *testing.T) {
	containers := []dockerContainer{
		{ID: "self000000000000", Names: []string{"/new-app-1"}, Labels: map[string]string{labelProject: "new"}},
		{ID: "sibling00000000", Names: []string{"/sibling-app-1"}, Labels: map[string]string{labelProject: "sibling"}},
	}
	volumes := []dockerVolume{
		{Name: "new_pgdata", Labels: map[string]string{labelProject: "new", labelVolume: "pgdata"}},
	}
	h := BuildHint("new", containers, volumes)
	if len(h.OldProjects) != 0 {
		t.Fatalf("old projects = %+v, want none (sibling has no pgdata volume)", h.OldProjects)
	}
}

func TestLogLinesCarryCopyCommands(t *testing.T) {
	h := &Hint{CurrentProject: "new", OldProjects: []Project{
		{Name: "old", Volume: "old_pgdata", DataDir: "/work/old/data"},
	}}
	lines := strings.Join(LogLines(h), "\n")
	for _, want := range []string{
		"old_pgdata",
		"/work/old/data",
		"COMPOSE_PROJECT_NAME=old docker compose up -d",
		"cp -a /work/old/data/. ./data/",
		"不会自动跨卷迁移",
	} {
		if !strings.Contains(lines, want) {
			t.Fatalf("warning missing %q:\n%s", want, lines)
		}
	}
}

func TestNoticeNamesOldVolume(t *testing.T) {
	h := &Hint{CurrentProject: "new", OldProjects: []Project{{Name: "old", Volume: "old_pgdata"}}}
	msg := Notice(h)
	if !strings.Contains(msg, "old_pgdata") || !strings.Contains(msg, "COMPOSE_PROJECT_NAME=old") {
		t.Fatalf("notice = %q", msg)
	}
	if Notice(&Hint{CurrentProject: "new"}) != "" {
		t.Fatal("empty hint must produce no notice")
	}
}

func TestHintFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if h, err := ReadHint(dir); err != nil || h != nil {
		t.Fatalf("missing hint = %+v, %v", h, err)
	}
	in := &Hint{CurrentProject: "new", OldProjects: []Project{{Name: "old", Volume: "old_pgdata"}}}
	if err := WriteHint(dir, in); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, DoneFile)); err != nil {
		t.Fatalf("done marker missing: %v", err)
	}
	out, err := ReadHint(dir)
	if err != nil || out == nil || len(out.OldProjects) != 1 || out.OldProjects[0].Volume != "old_pgdata" {
		t.Fatalf("round trip = %+v, %v", out, err)
	}
	if err := ClearHint(dir); err != nil {
		t.Fatal(err)
	}
	if out, err := ReadHint(dir); err != nil || out != nil {
		t.Fatalf("after clear = %+v, %v", out, err)
	}
}

func TestDisplayPathStripsDockerDesktopPrefix(t *testing.T) {
	if got := displayPath("/host_mnt/private/tmp/old/data"); got != "/private/tmp/old/data" {
		t.Fatalf("host_mnt path = %q", got)
	}
	if got := displayPath("/srv/old/data"); got != "/srv/old/data" {
		t.Fatalf("linux path = %q", got)
	}
}
