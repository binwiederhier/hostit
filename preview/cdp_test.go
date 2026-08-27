package preview

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeChrome speaks just enough DevTools protocol to drive capture(): it
// answers /json/list with one page target, replies to commands, and fires the
// load event when told to.
type fakeChrome struct {
	server *httptest.Server
	// fireLoad controls whether the fake ever reports the page as loaded --
	// false exercises the "page never fires load" path.
	fireLoad bool
	// navigated records the URL the client asked for.
	navigated chan string
	pngBytes  []byte
}

func newFakeChrome(t *testing.T, fireLoad bool) *fakeChrome {
	t.Helper()
	f := &fakeChrome{fireLoad: fireLoad, navigated: make(chan string, 1), pngBytes: []byte("\x89PNG\r\n\x1a\nfake")}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		ws := "ws" + strings.TrimPrefix(f.server.URL, "http") + "/devtools/page/1"
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"type": "background_page", "webSocketDebuggerUrl": "ws://ignored"},
			{"type": "page", "webSocketDebuggerUrl": ws},
		})
	})
	mux.HandleFunc("/devtools/page/1", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		conn.SetReadLimit(cdpReadLimit)
		ctx := r.Context()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var msg struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal(data, &msg) != nil {
				return
			}
			reply := func(v any) {
				body, _ := json.Marshal(v)
				_ = conn.Write(ctx, websocket.MessageText, body)
			}
			switch msg.Method {
			case "Page.enable":
				reply(map[string]any{"id": msg.ID, "result": map[string]any{}})
			case "Page.navigate":
				url, _ := msg.Params["url"].(string)
				select {
				case f.navigated <- url:
				default:
				}
				reply(map[string]any{"id": msg.ID, "result": map[string]any{"frameId": "1"}})
				// An unrelated event first: the client must skip past it.
				reply(map[string]any{"method": "Page.frameStartedLoading", "params": map[string]any{}})
				if f.fireLoad {
					reply(map[string]any{"method": "Page.loadEventFired", "params": map[string]any{}})
				}
			case "Page.captureScreenshot":
				reply(map[string]any{"id": msg.ID, "result": map[string]any{
					"data": base64.StdEncoding.EncodeToString(f.pngBytes),
				}})
			default:
				reply(map[string]any{"id": msg.ID, "result": map[string]any{}})
			}
		}
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func TestCaptureNavigatesWaitsAndShoots(t *testing.T) {
	t.Parallel()
	f := newFakeChrome(t, true)
	start := time.Now()
	png, err := capture(context.Background(), f.server.URL, "https://blog.example.com/", 200*time.Millisecond, nil)
	require.NoError(t, err)
	assert.Equal(t, f.pngBytes, png)
	assert.Equal(t, "https://blog.example.com/", <-f.navigated)
	assert.GreaterOrEqual(t, time.Since(start), 200*time.Millisecond,
		"the settle window is REAL time -- that is the whole point of not using --virtual-time-budget")
}

// A page that never fires load must still be captured once the settle window
// is up: one stuck subresource should cost a card its freshness, not its shot.
func TestCaptureShootsEvenWithoutTheLoadEvent(t *testing.T) {
	t.Parallel()
	f := newFakeChrome(t, false)
	png, err := capture(context.Background(), f.server.URL, "https://slow.example.com/", 150*time.Millisecond, nil)
	require.NoError(t, err)
	assert.Equal(t, f.pngBytes, png)
}

func TestCaptureFailsWhenChromeNeverAnswers(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := capture(ctx, "http://127.0.0.1:1", "https://blog.example.com/", time.Second, nil)
	require.Error(t, err)
}
