package server

import (
	"net/http"
)

// registerAssetsRoutes registers the assets HTTP routes. Paths, methods and
// handlers are unchanged from the pre-split table.
func (s *Server) registerAssetsRoutes(mux *http.ServeMux) {
	// asset system
	mux.HandleFunc("GET /api/assets", s.listAssets)

	mux.HandleFunc("GET /api/assets/counts", s.assetCounts)

	mux.HandleFunc("POST /api/assets", s.insertAssets)

	mux.HandleFunc("DELETE /api/assets", s.deleteAssets)

	// company system
	mux.HandleFunc("GET /api/companies", s.listCompanies)

	mux.HandleFunc("POST /api/companies", s.createCompany)

	mux.HandleFunc("GET /api/companies/{id}", s.getCompany)

	mux.HandleFunc("DELETE /api/companies/{id}", s.deleteCompany)

	mux.HandleFunc("POST /api/companies/{id}/scope", s.addCompanyScope)

	mux.HandleFunc("POST /api/companies/reattribute", s.reattribute)

	// MCP 资源侧可见性（更具体的 skill 路由会优先匹配）
	mux.HandleFunc("GET /api/visibility/{kind}/{id}", s.pgResourceVisibility)

	mux.HandleFunc("POST /api/visibility/toggle", s.pgToggleVisibility)

	// Skill 可见性（按名称，更具体，优先于上面的通配路由）
	mux.HandleFunc("GET /api/visibility/skill/{name}", s.pgSkillVisibility)

	mux.HandleFunc("POST /api/visibility/skill/toggle", s.pgToggleSkillVisibility)

	// 资产拦截规则管理（全局黑名单：域名/IP/URL/CIDR）
	mux.HandleFunc("GET /api/asset-intercept/rules", s.assetInterceptListRules)

	mux.HandleFunc("POST /api/asset-intercept/rules", s.assetInterceptCreateRule)

	mux.HandleFunc("PUT /api/asset-intercept/rules/{id}", s.assetInterceptUpdateRule)

	mux.HandleFunc("DELETE /api/asset-intercept/rules/{id}", s.assetInterceptDeleteRule)

	mux.HandleFunc("POST /api/asset-intercept/rules/{id}/toggle", s.assetInterceptToggleRule)

}

func (s *Server) pgSkillVisibility(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	agents, err := pg.SkillAgents(r.PathValue("name"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"agents": idStrings(agents)})
}

func (s *Server) pgToggleSkillVisibility(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		AgentID   int64  `json:"agent_id,string"`
		SkillName string `json:"skill_name"`
		Visible   bool   `json:"visible"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := pg.ToggleSkillVisibility(body.AgentID, body.SkillName, body.Visible); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) pgResourceVisibility(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, _ := pathInt(r, "id")
	agents, err := pg.ResourceAgents(r.PathValue("kind"), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"agents": idStrings(agents)})
}

func (s *Server) pgToggleVisibility(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		AgentID    int64  `json:"agent_id,string"`
		Kind       string `json:"kind"`
		ResourceID int64  `json:"resource_id"`
		Visible    bool   `json:"visible"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := pg.ToggleVisibility(body.AgentID, body.Kind, body.ResourceID, body.Visible); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
