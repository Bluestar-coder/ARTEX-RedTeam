package server

import (
	"net/http"
)

// registerFindingsRoutes registers the findings HTTP routes. Paths, methods and
// handlers are unchanged from the pre-split table.
func (s *Server) registerFindingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PATCH /api/triggers/{id}", s.pgUpdateTrigger)

	mux.HandleFunc("DELETE /api/triggers/{id}", s.pgDeleteTrigger)

	s.registerFindingTraffic(mux)
}

func (s *Server) registerFindingTraffic(mux *http.ServeMux) {
	base := "/api/exploration/findings/{id}/traffic"
	mux.HandleFunc("GET "+base, s.getFindingTraffic)
	mux.HandleFunc("POST "+base, s.bindFindingTraffic)
	mux.HandleFunc("PATCH "+base+"/{binding_id}", s.editFindingTraffic)
	mux.HandleFunc("DELETE "+base+"/{binding_id}", s.editFindingTraffic)
	mux.HandleFunc("PUT "+base+"/order", s.editFindingTraffic)
	mux.HandleFunc("GET "+base+"/{binding_id}", s.getFindingTrafficDetail)
	mux.HandleFunc("GET "+base+"/{binding_id}/body", s.getFindingTrafficBody)
}
