package server

import (
	"encoding/json"
	"net/http"
	"time"

	"heckel.io/hostit/store"
)

// apiAddAppDomainRequest is the body of POST /api/apps/{name}/domains
type apiAddAppDomainRequest struct {
	Domain string `json:"domain"`
}

// apiAppDomainResponse is one custom domain, with the DNS records the owner must
// create so it routes and can obtain a certificate
type apiAppDomainResponse struct {
	Domain    string      `json:"domain"`
	Status    string      `json:"status"`
	LastError string      `json:"last_error,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	ActiveAt  *time.Time  `json:"active_at,omitempty"`
	DNS       []dnsRecord `json:"dns"`
}

func (s *Server) domainView(appName string, d *store.Domain) *apiAppDomainResponse {
	return &apiAppDomainResponse{
		Domain:    d.Domain,
		Status:    string(d.Status),
		LastError: d.LastError,
		CreatedAt: d.CreatedAt,
		ActiveAt:  d.ActiveAt,
		DNS:       s.DomainDNSRecords(appName, d.Domain),
	}
}

// handleAppDomainsList returns an app's custom domains.
func (s *Server) handleAppDomainsList(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	domains, err := s.AppDomains(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	out := make([]*apiAppDomainResponse, 0, len(domains))
	for _, d := range domains {
		out = append(out, s.domainView(a.Name, d))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAppDomainAdd attaches a custom domain and starts certificate issuance.
func (s *Server) handleAppDomainAdd(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	var req apiAddAppDomainRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	d, err := s.AddAppDomain(a.Name, req.Domain)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.domainView(a.Name, d))
}

// handleAppDomainVerify re-attempts certificate issuance after the owner set up DNS.
func (s *Server) handleAppDomainVerify(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	if err := s.VerifyAppDomain(a.Name, r.PathValue("domain")); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "verifying"})
}

// handleAppDomainDelete detaches a custom domain.
func (s *Server) handleAppDomainDelete(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	if err := s.RemoveAppDomain(a.Name, r.PathValue("domain")); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "removed"})
}
