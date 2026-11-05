package server

import (
	"net/http"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
	"github.com/Bluestar-coder/ARTEX-RedTeam/report"
)

// registerReportRoutes registers the report HTTP routes. Paths, methods and
// handlers are unchanged from the pre-split table.
func (s *Server) registerReportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/report", s.getReport)

}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "no active task")
		return
	}
	findings, _ := t.Store.ListByKind(db.KindFinding, 1000) // 纯漏洞（事实是独立的 KindFact，不进报告）
	counts := map[string]int{}
	for _, ty := range []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"} {
		ns, _ := s.m.Assets().QueryByType(ty, 100000, 0)
		if len(ns) > 0 {
			counts[ty] = len(ns)
		}
	}
	md := report.Markdown(report.Input{
		Title: t.Description, Goal: t.Goal, GeneratedAt: time.Now(),
		AssetCounts: counts, Findings: findings,
	})
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(md))
}
