package proxylink

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"heckel.io/hostit/cluster"
	"heckel.io/hostit/proxyapi"
)

// The ProxyAgent RPC: hostit-proxy serves its agent behind RPCHandler,
// hostit-control talks to it through NewRemoteProxy, and the proxy's own
// requests go the other way through CallbackHandler. Two verbs one way and one
// the other -- the whole proxy contract fits on a page, which is the point.

const (
	// applyRoutesPath/heartbeatPath are what control calls on a proxy;
	// certPath is what a proxy calls on control.
	applyRoutesPath = "/v1/apply_routes"
	heartbeatPath   = "/v1/heartbeat"
	certPath        = "/v1/cert"
	// noCertCode marks the "not ours" answer so it survives the wire as a
	// sentinel rather than as prose the caller would have to string-match.
	noCertCode = "no_cert"
)

// certReq/certResp are the reverse channel's envelope.
type certReq struct {
	SNI string `json:"sni"`
}

type certResp struct {
	Material *proxyapi.CertMaterial `json:"material,omitempty"`
	Err      string                 `json:"err,omitempty"`
	ErrCode  string                 `json:"err_code,omitempty"`
}

// RPCHandler serves a ProxyAgent over the duplex: control's calls arrive here.
func RPCHandler(agent proxyapi.ProxyAgent) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+applyRoutesPath, func(w http.ResponseWriter, r *http.Request) {
		var table proxyapi.Table
		if err := json.NewDecoder(r.Body).Decode(&table); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := agent.ApplyRoutes(&table); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST "+heartbeatPath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, agent.Heartbeat())
	})
	return mux
}

// NewRemoteProxy is control's view of a connected proxy: the ProxyAgent
// interface, carried over that proxy's own connection.
func NewRemoteProxy(client *http.Client) proxyapi.ProxyAgent {
	return &remoteProxy{client: client}
}

type remoteProxy struct {
	client *http.Client
}

func (r *remoteProxy) ApplyRoutes(table *proxyapi.Table) error {
	body, err := json.Marshal(table)
	if err != nil {
		return err
	}
	resp, err := r.post(applyRoutesPath, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("apply routes: %s", resp.Status)
	}
	return nil
}

func (r *remoteProxy) Heartbeat() *proxyapi.Heartbeat {
	resp, err := r.post(heartbeatPath, nil)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var hb proxyapi.Heartbeat
	if err := json.NewDecoder(resp.Body).Decode(&hb); err != nil {
		return nil
	}
	return &hb
}

func (r *remoteProxy) post(path string, body []byte) (*http.Response, error) {
	// The host is cosmetic: every request rides its own stream on the session
	// the proxy dialed.
	return r.client.Post("http://"+cluster.ControlID+path, "application/json", bytes.NewReader(body))
}

// CallbackHandler is control's side of the reverse channel: what a proxy may
// ask control for over its own connection.
func CallbackHandler(sink proxyapi.ControlSink) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+certPath, func(w http.ResponseWriter, r *http.Request) {
		var req certReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, &certResp{Err: "bad request: " + err.Error()})
			return
		}
		mat, err := sink.CertFor(req.SNI)
		if err != nil {
			resp := &certResp{Err: err.Error()}
			if errors.Is(err, proxyapi.ErrNoCert) {
				resp.ErrCode = noCertCode
			}
			writeJSON(w, resp)
			return
		}
		writeJSON(w, &certResp{Material: mat})
	})
	return mux
}

// NewControlSink is the proxy's view of control, over the connection it dialed.
func NewControlSink(client *http.Client) proxyapi.ControlSink {
	return &controlSink{client: client}
}

type controlSink struct {
	client *http.Client
}

func (c *controlSink) CertFor(sni string) (*proxyapi.CertMaterial, error) {
	body, err := json.Marshal(&certReq{SNI: sni})
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Post("http://"+cluster.ControlID+certPath, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out certResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.ErrCode == noCertCode {
		return nil, proxyapi.ErrNoCert
	}
	if out.Err != "" {
		return nil, errors.New(out.Err)
	}
	if out.Material == nil {
		return nil, proxyapi.ErrNoCert
	}
	return out.Material, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
