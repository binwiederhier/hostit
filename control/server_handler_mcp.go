package control

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"crypto/rand"

	"heckel.io/hostit/control/connections"
	"heckel.io/hostit/control/mcp"
	"heckel.io/hostit/store"
)

// The MCP endpoints, in three groups: the public client metadata document an
// authorization server fetches, the owner-facing tool list, and the app-facing
// pair (what tools are there, run one).

// apiMCPTool is one tool, as both the UI and an app see it.
type apiMCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type apiMCPToolsResponse struct {
	Tools []apiMCPTool `json:"tools"`
}

// apiMCPCallRequest is what an app sends to run a tool.
type apiMCPCallRequest struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// apiMCPCallResponse is the outcome. IsError means the TOOL failed, which is
// not an HTTP failure: the call happened and the answer is bad news, and an app
// wants to show that rather than retry it.
type apiMCPCallResponse struct {
	Text    string `json:"text"`
	IsError bool   `json:"is_error,omitempty"`
}

func mcpToolViews(tools []mcp.Tool) []apiMCPTool {
	out := make([]apiMCPTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, apiMCPTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

// handleMCPClientMetadata publishes the Client ID Metadata Document. An
// authorization server hostit has never spoken to fetches this at the client_id
// URL to learn who is asking, which is what replaced dynamic registration.
// Public and unauthenticated by necessity.
func (s *Server) handleMCPClientMetadata(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
	doc := mcp.ClientMetadata(s.config.RedirectURL(host), s.mcpClientID(r))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(doc)
}

// handleConnectionMCPTools re-reads what an MCP connection's server offers, for
// the owner deciding what to grant.
func (s *Server) handleConnectionMCPTools(w http.ResponseWriter, r *http.Request, c *caller) {
	conn, err := s.ownedConnection(c, r.PathValue("slug"))
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	if conn.Kind != store.ConnectionMCP {
		writeMCPError(w, errNotMCP)
		return
	}
	tools, err := s.connections.mcpTools(r.Context(), conn, s.mcpClientID(r))
	if err != nil {
		writeMCPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMCPToolsResponse{Tools: mcpToolViews(tools)})
}

// startMCPConsent sends the owner to the server's authorization server, holding
// the PKCE verifier here rather than in a cookie.
func (s *Server) startMCPConsent(w http.ResponseWriter, r *http.Request, userID, slug, label, serverURL string, disco mcp.Discovery) (string, error) {
	// Worked out BEFORE the browser leaves: a server that will not accept hostit
	// as a client must say so here, where the owner can read it, rather than as
	// an opaque error on somebody else's consent screen.
	clientID, err := mcp.ClientIDFor(r.Context(), s.connections.client, disco,
		s.config.RedirectURL(hostOnly(r.Host)), s.mcpClientID(r))
	if err != nil {
		// NOT an invalid credential: the owner typed nothing wrong. The server
		// will not have hostit as a client, and saying "invalid credential"
		// sends them looking for a mistake that is not theirs.
		return "", fmt.Errorf("%w: %s", errMCPUnusable, err)
	}
	pkce, err := mcp.NewPKCE()
	if err != nil {
		return "", err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	nonce := base64.RawURLEncoding.EncodeToString(raw)
	s.mcp.put(nonce, mcpPending{
		userID: userID, slug: slug, label: label, serverURL: serverURL,
		discovery: disco, clientID: clientID, pkce: pkce, expires: time.Now().Add(mcpPendingTTL),
	})
	// The same state shape the catalog providers use, so one callback handles
	// both and no second redirect URI has to be registered anywhere.
	state := connectStatePrefix + connections.ProviderMCP + ":" + slug + ":" + nonce
	http.SetCookie(w, s.cookie(s.cookieName(stateCookieName), state, int(mcpPendingTTL.Seconds())))
	return mcp.AuthCodeURL(disco, clientID, s.config.RedirectURL(hostOnly(r.Host)), state, pkce), nil
}

// finishMCPConsent redeems the code that came back from the authorization
// server. Reports whether it took the request.
func (s *Server) finishMCPConsent(w http.ResponseWriter, r *http.Request, userID, nonce, code string) {
	pending, ok := s.mcp.take(nonce)
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("that connection attempt has expired; start it again"))
		return
	}
	// Whoever started the consent is the only one who may finish it, even
	// though the state came back through their own browser.
	if pending.userID != userID {
		writeError(w, http.StatusForbidden, errors.New("that connection attempt belongs to someone else"))
		return
	}
	tok, err := mcp.Exchange(r.Context(), s.connections.client, pending.discovery,
		pending.clientID, s.config.RedirectURL(hostOnly(r.Host)), code, pending.pkce)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if err := s.connections.saveMCPToken(r.Context(), pending, tok); err != nil {
		writeConnectionError(w, err)
		return
	}
	http.Redirect(w, r, "/connections", http.StatusFound)
}

// ---- The app-facing half, over the app's own socket -----------------------

// handleSelfMCPTools tells an app what a granted MCP server can do, so an agent
// building the app can discover the tools rather than be told them.
func (s *Server) handleSelfMCPTools(w http.ResponseWriter, r *http.Request, a *store.App) {
	if s.connections == nil {
		writeError(w, http.StatusNotImplemented, errors.New("connections are not available on this server"))
		return
	}
	conn, err := s.connections.grantedMCPConnection(a, r.PathValue("slug"))
	if err != nil {
		writeMCPError(w, err)
		return
	}
	tools, err := s.connections.mcpTools(r.Context(), conn, s.selfMCPClientID())
	if err != nil {
		writeMCPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMCPToolsResponse{Tools: mcpToolViews(tools)})
}

// handleSelfMCPCall runs one tool on a granted server. The app sends a name and
// arguments; hostit holds the token and makes the call, so the app never gets a
// credential that would open the whole server.
func (s *Server) handleSelfMCPCall(w http.ResponseWriter, r *http.Request, a *store.App) {
	if s.connections == nil {
		writeError(w, http.StatusNotImplemented, errors.New("connections are not available on this server"))
		return
	}
	var req apiMCPCallRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Tool == "" {
		writeError(w, http.StatusBadRequest, errors.New("which tool? send {\"tool\":\"...\",\"arguments\":{...}}"))
		return
	}
	// Resolved BEFORE anything is sent anywhere: an app without the grant must
	// not be able to make hostit contact the server on its behalf at all.
	conn, err := s.connections.grantedMCPConnection(a, r.PathValue("slug"))
	if err != nil {
		writeMCPError(w, err)
		return
	}
	res, err := s.connections.mcpCall(r.Context(), conn, s.selfMCPClientID(), req.Tool, req.Arguments)
	if err != nil {
		writeMCPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMCPCallResponse{Text: res.Text, IsError: res.IsError})
}

// selfMCPClientID is the client id for a call that did not arrive over HTTP and
// so has no request host to build one from. It must be the SAME URL the consent
// used, or the authorization server refuses the refresh.
func (s *Server) selfMCPClientID() string {
	return s.config.WebURL(s.config.APIHostname()) + mcpClientMetadataPath
}

// writeMCPError maps the MCP failures onto status codes that mean something to
// whoever has to fix them.
func writeMCPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotConnected):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, errNotGranted):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, errNotMCP):
		// The mirror of the token endpoint refusing an MCP member: a credential
		// has no tools sub-resource, and "that does not exist here" is one
		// status code, not two.
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, mcp.ErrUnauthorized):
		// The remedy is the owner reconnecting, so say so rather than leaving a
		// 502 that looks like the server is down.
		writeError(w, http.StatusBadGateway, fmt.Errorf("%w -- reconnect it on the Connections page", err))
	default:
		writeAppError(w, err)
	}
}
