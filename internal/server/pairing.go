package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// handleCreatePairing is intentionally unauthenticated; the returned secret is the only proof of ownership.
func (s *Server) handleCreatePairing(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hostname      string `json:"hostname"`
		OS            string `json:"os"`
		DaemonVersion string `json:"daemon_version"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	t, err := s.svc.CreatePairing(r.Context(), body.Hostname, body.OS, body.DaemonVersion)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"pairing": map[string]any{"id": t.ID, "code": t.Code, "expires_at": t.ExpiresAt},
		"secret":  t.Secret,
	})
}

func (s *Server) handlePollPairing(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.PollPairing(r.Context(), chi.URLParam(r, "pairingID"), bearerToken(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleViewPairing(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requireUser(r); err != nil {
		writeError(w, err)
		return
	}
	p, err := s.svc.ViewPairing(r.Context(), chi.URLParam(r, "pairingID"), r.URL.Query().Get("code"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairing": p})
}

func (s *Server) handleConfirmPairing(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	comp, err := s.svc.ConfirmPairing(r.Context(), actor, chi.URLParam(r, "pairingID"), body.Code, body.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"computer": comp})
}
