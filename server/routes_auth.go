package server

import (
	"net/http"
)

// registerAuthRoutes registers the auth HTTP routes. Paths, methods and
// handlers are unchanged from the pre-split table.
func (s *Server) registerAuthRoutes(mux *http.ServeMux) {
	// Auth routes. status/init/login are on the exact publicAPIPaths allowlist;
	// change-password goes through the normal JWT check. All three state-changing
	// endpoints are rate-limited per client IP so a burst cannot turn a transient
	// database error into an initialization window.
	mux.HandleFunc("GET /api/auth/status", s.authStatus)

	mux.HandleFunc("POST /api/auth/init", s.limitAuth(s.authInit))

	mux.HandleFunc("POST /api/auth/login", s.limitAuth(s.authLogin))

	mux.HandleFunc("POST /api/auth/change-password", s.limitAuth(s.authChangePassword))

}
