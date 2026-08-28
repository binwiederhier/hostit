package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"heckel.io/hostit/connections"
	"heckel.io/hostit/http/outbound"
	"heckel.io/hostit/store"
)

// Provider definitions over the API. Two scopes reach the same handlers:
// "personal" (any active user, their own) and "instance" (an admin, everyone's).
// The scope is where the danger lives, so it is decided here and once.

const maxProvidersPerUser = 25

// apiProviderDefResponse is one definition. The client SECRET is never in it --
// the id is public by design, the secret is not, and a UI that needs to show
// "configured" can read HasSecret.
type apiProviderDefResponse struct {
	Name       string            `json:"name"`
	Label      string            `json:"label"`
	Kind       string            `json:"kind"`
	Scope      string            `json:"scope"` // "instance" or "personal"
	Scopes     []string          `json:"scopes,omitempty"`
	Issuer     string            `json:"issuer,omitempty"`
	AuthURL    string            `json:"auth_url,omitempty"`
	TokenURL   string            `json:"token_url,omitempty"`
	ClientID   string            `json:"client_id,omitempty"`
	HasSecret  bool              `json:"has_secret"`
	AuthParams map[string]string `json:"auth_params,omitempty"`
	LongLived  bool              `json:"long_lived,omitempty"`
	URL        string            `json:"url,omitempty"`
	Help       string            `json:"help,omitempty"`
	NameHint   string            `json:"name_hint,omitempty"`
	// Editable is false for hostit's own catalog and for control.yml entries:
	// both exist outside the database and cannot be changed through the API.
	Editable  bool      `json:"editable"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	// RedirectURI is what the owner must register with the vendor. Returned
	// with the list because it is the one piece nobody can work out themselves,
	// and getting it wrong is the most common reason a consent fails.
	RedirectURI string `json:"redirect_uri,omitempty"`
}

type apiProvidersResponse struct {
	Providers []*apiProviderDefResponse `json:"providers"`
	// RedirectURI is the callback to register with any vendor, for this host.
	RedirectURI string `json:"redirect_uri"`
}

// apiProviderRequest creates or replaces a definition.
type apiProviderRequest struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	// Scope is "personal" (the default) or "instance", which requires an admin.
	Scope        string            `json:"scope,omitempty"`
	Scopes       []string          `json:"scopes,omitempty"`
	Issuer       string            `json:"issuer,omitempty"`
	AuthURL      string            `json:"auth_url,omitempty"`
	TokenURL     string            `json:"token_url,omitempty"`
	ClientID     string            `json:"client_id,omitempty"`
	ClientSecret string            `json:"client_secret,omitempty"`
	AuthParams   map[string]string `json:"auth_params,omitempty"`
	LongLived    bool              `json:"long_lived,omitempty"`
	URL          string            `json:"url,omitempty"`
	Help         string            `json:"help,omitempty"`
	NameHint     string            `json:"name_hint,omitempty"`
}

// handleProvidersList returns every definition this caller can see, at every
// tier, so the UI can show what is theirs to change and what is not.
func (s *Server) handleProvidersList(w http.ResponseWriter, r *http.Request, c *caller) {
	if s.connections == nil {
		writeError(w, http.StatusNotImplemented, errors.New("connections are not available on this server"))
		return
	}
	redirect := s.config.RedirectURL(hostOnly(r.Host))
	out := &apiProvidersResponse{Providers: make([]*apiProviderDefResponse, 0), RedirectURI: redirect}

	// Tier 1 and 2: the catalog and control.yml. Neither is editable here.
	for _, p := range s.connections.offered() {
		if p.Kind != connections.KindOAuth {
			continue
		}
		out.Providers = append(out.Providers, &apiProviderDefResponse{
			Name: p.Name, Label: p.Label, Kind: store.ProviderOAuth, Scope: "instance",
			Scopes: p.Scopes, AuthURL: p.AuthURL, TokenURL: p.TokenURL, Issuer: p.Issuer,
			HasSecret: true, Help: p.Help, NameHint: p.NameHint, Editable: false,
			RedirectURI: redirect,
		})
	}
	// Tier 2 and 3 from the database.
	rows, err := s.apps.Store().ProvidersFor(c.userID())
	if err != nil {
		writeAppError(w, err)
		return
	}
	for _, row := range rows {
		out.Providers = append(out.Providers, providerRowView(row, c, redirect))
	}
	writeJSON(w, http.StatusOK, out)
}

func providerRowView(row *store.Provider, c *caller, redirect string) *apiProviderDefResponse {
	scope := "instance"
	if row.OwnerID != "" {
		scope = "personal"
	}
	var params map[string]string
	if row.AuthParams != "" {
		_ = json.Unmarshal([]byte(row.AuthParams), &params)
	}
	return &apiProviderDefResponse{
		Name: row.Name, Label: row.Label, Kind: row.Kind, Scope: scope,
		Scopes: splitScopes(row.Scopes), Issuer: row.Issuer,
		AuthURL: row.AuthURL, TokenURL: row.TokenURL, ClientID: row.ClientID,
		HasSecret: row.ClientSecret != "", AuthParams: params, LongLived: row.LongLived,
		URL: row.URL, Help: row.Help, NameHint: row.NameHint,
		// An instance definition is the admin's to change; a personal one is
		// its owner's. Nobody edits somebody else's.
		Editable:    (row.OwnerID == "" && c.isAdmin()) || row.OwnerID == c.userID(),
		CreatedAt:   row.CreatedAt,
		RedirectURI: redirect,
	}
}

// handleProviderAdd defines a provider. Personal by default; "instance"
// requires an admin, because that one changes what a name means for everybody.
func (s *Server) handleProviderAdd(w http.ResponseWriter, r *http.Request, c *caller) {
	s.saveProviderFromRequest(w, r, c, "")
}

// handleProviderUpdate replaces one, keeping its name.
func (s *Server) handleProviderUpdate(w http.ResponseWriter, r *http.Request, c *caller) {
	s.saveProviderFromRequest(w, r, c, r.PathValue("name"))
}

func (s *Server) saveProviderFromRequest(w http.ResponseWriter, r *http.Request, c *caller, existingName string) {
	if s.connections == nil {
		writeError(w, http.StatusNotImplemented, errors.New("connections are not available on this server"))
		return
	}
	var req apiProviderRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if existingName != "" {
		req.Name = existingName
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	ownerID := c.userID()
	if req.Scope == "instance" {
		if !c.isAdmin() {
			writeError(w, http.StatusForbidden, errors.New("only an administrator defines a provider for the whole instance"))
			return
		}
		ownerID = ""
	} else if ownerID == "" {
		// The admin token has no user record, and an empty owner IS the marker
		// for an instance provider -- so a request that said "personal" would
		// silently define one for everybody. Refused rather than guessed.
		writeError(w, http.StatusForbidden, errors.New(
			`a personal provider belongs to a person, and the admin token is not one: `+
				`sign in as a user, or send "scope":"instance" to define one for everybody`))
		return
	}

	kind := req.Kind
	if kind == "" {
		kind = store.ProviderOAuth
	}
	var existing *store.Provider
	if existingName != "" {
		row, err := s.apps.Store().ProviderByName(c.userID(), name)
		if err != nil {
			writeProviderError(w, err)
			return
		}
		if !((row.OwnerID == "" && c.isAdmin()) || row.OwnerID == c.userID()) {
			writeError(w, http.StatusForbidden, errors.New("that provider is not yours to change"))
			return
		}
		existing, ownerID = row, row.OwnerID
	} else {
		if err := s.connections.nameAvailableFor(ownerID, name, kind, ""); err != nil {
			writeProviderError(w, err)
			return
		}
		mine, err := s.apps.Store().ProvidersFor(c.userID())
		if err == nil && len(mine) >= maxProvidersPerUser {
			writeError(w, http.StatusForbidden, errors.New("you already have the maximum number of providers"))
			return
		}
	}

	row, err := s.providerRowFrom(name, ownerID, req)
	if err != nil {
		writeProviderError(w, err)
		return
	}
	if err := s.connections.saveProvider(row, req.ClientSecret, existing); err != nil {
		writeProviderError(w, err)
		return
	}
	status := http.StatusCreated
	if existing != nil {
		status = http.StatusOK
	}
	writeJSON(w, status, providerRowView(row, c, s.config.RedirectURL(hostOnly(r.Host))))
}

// providerRowFrom validates a submitted definition into a row. It refuses a
// half-written one HERE, where the person is looking at the form, rather than
// accepting it and failing on somebody's consent screen.
func (s *Server) providerRowFrom(name, ownerID string, req apiProviderRequest) (*store.Provider, error) {
	kind := req.Kind
	if kind == "" {
		kind = store.ProviderOAuth
	}
	row := &store.Provider{
		OwnerID: ownerID, Name: name, Label: strings.TrimSpace(req.Label), Kind: kind,
		Help: req.Help, NameHint: req.NameHint,
	}
	switch kind {
	case store.ProviderMCP:
		if err := outbound.CheckURL(req.URL); err != nil {
			return nil, err
		}
		if row.Label == "" {
			return nil, errors.New("an MCP server needs a label: it is what a person picks from the menu")
		}
		row.URL = strings.TrimSpace(req.URL)
	case store.ProviderOAuth:
		// Validated by the same code a control.yml entry goes through, so the
		// rules cannot drift between the two ways of defining one.
		if _, err := connections.CustomProvider(name, connections.CustomSpec{
			Label: row.Label, Scopes: req.Scopes, Issuer: req.Issuer,
			AuthURL: req.AuthURL, TokenURL: req.TokenURL,
			AuthParams: req.AuthParams, LongLivedToken: req.LongLived,
		}); err != nil {
			return nil, err
		}
		if req.ClientID == "" {
			return nil, errors.New("an OAuth provider needs the client ID you registered with that service")
		}
		for _, u := range []string{req.Issuer, req.AuthURL, req.TokenURL} {
			if u == "" {
				continue
			}
			if err := outbound.CheckURL(u); err != nil {
				return nil, err
			}
			// Plaintext OAuth would exchange the client secret and code in the
			// clear; require https unless this instance has opted into private
			// outbound (the self-hosted-dev case that also allows loopback).
			if strings.HasPrefix(u, "http://") && len(s.config.OutboundAllowPrivateCIDRs) == 0 {
				return nil, fmt.Errorf("%s must use https://; plain http is only accepted where private outbound is enabled", u)
			}
		}
		row.Scopes = strings.Join(req.Scopes, " ")
		row.Issuer, row.AuthURL, row.TokenURL = req.Issuer, req.AuthURL, req.TokenURL
		row.ClientID, row.LongLived = req.ClientID, req.LongLived
		if len(req.AuthParams) > 0 {
			b, err := json.Marshal(req.AuthParams)
			if err != nil {
				return nil, err
			}
			row.AuthParams = string(b)
		}
	default:
		return nil, errors.New(`a provider is "oauth" or "mcp"`)
	}
	return row, nil
}

// handleProviderDelete forgets a definition.
func (s *Server) handleProviderDelete(w http.ResponseWriter, r *http.Request, c *caller) {
	if s.connections == nil {
		writeError(w, http.StatusNotImplemented, errors.New("connections are not available on this server"))
		return
	}
	row, err := s.apps.Store().ProviderByName(c.userID(), strings.ToLower(strings.TrimSpace(r.PathValue("name"))))
	if err != nil {
		writeProviderError(w, err)
		return
	}
	if !((row.OwnerID == "" && c.isAdmin()) || row.OwnerID == c.userID()) {
		writeError(w, http.StatusForbidden, errors.New("that provider is not yours to remove"))
		return
	}
	if err := s.apps.Store().DeleteProvider(row.ID); err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "removed"})
}

func writeProviderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrProviderNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, store.ErrProviderExists):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, connections.ErrInvalidCredential), errors.Is(err, outbound.ErrBadScheme):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}
