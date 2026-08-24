package agent

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The loopback proxy must hand the request to whatever listens on the unix
// socket, unchanged (method, path, body), and hand the response back.
func TestAPIProxyForwardsToTheUnixSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "hostit.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()

	var gotMethod, gotPath, gotBody string
	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("X-From", "socket")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})}
	go backend.Serve(ln)
	defer backend.Close()

	front := httptest.NewServer(apiProxyHandler(sock))
	defer front.Close()

	req, _ := http.NewRequest(http.MethodPost, front.URL+"/api/container/self", strings.NewReader(`{"q":1}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if gotMethod != http.MethodPost || gotPath != "/api/container/self" || gotBody != `{"q":1}` {
		t.Fatalf("backend saw method=%q path=%q body=%q; want POST /api/container/self {\"q\":1}", gotMethod, gotPath, gotBody)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) || resp.Header.Get("X-From") != "socket" {
		t.Fatalf("response not forwarded back: status=%d hdr=%q body=%q", resp.StatusCode, resp.Header.Get("X-From"), string(body))
	}
}
