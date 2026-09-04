package screenshot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	stdimage "image"
	"image/color"
	"image/draw"
	"image/png"
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
	// lifecycle are the Page.lifecycleEvent names to emit after navigate, so a
	// test can drive the paint/network-idle signals chrome really sends. Empty
	// means none, which exercises the frame-polling fallback.
	lifecycle []string
	// navigated records the URL the client asked for.
	navigated chan string
	pngBytes  []byte
	// frames, when set, is returned one-per-capture (holding the last), so a test
	// can exercise the settle's poll-until-stable-and-non-blank behavior.
	frames   [][]byte
	frameIdx int
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
				for _, name := range f.lifecycle {
					reply(map[string]any{"method": "Page.lifecycleEvent", "params": map[string]any{"name": name}})
				}
				if f.fireLoad {
					reply(map[string]any{"method": "Page.loadEventFired", "params": map[string]any{}})
				}
			case "Page.captureScreenshot":
				if len(f.frames) > 0 {
					i := f.frameIdx
					if i >= len(f.frames) {
						i = len(f.frames) - 1
					}
					f.frameIdx++
					reply(map[string]any{"id": msg.ID, "result": map[string]any{"data": base64.StdEncoding.EncodeToString(f.frames[i])}})
					continue
				}
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

func TestIsMostlyBlank(t *testing.T) {
	// A helper to PNG-encode an image.
	enc := func(img stdimage.Image) []byte {
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		return buf.Bytes()
	}
	// All-white frame -> blank.
	white := stdimage.NewRGBA(stdimage.Rect(0, 0, 100, 100))
	draw.Draw(white, white.Bounds(), stdimage.NewUniform(color.White), stdimage.Point{}, draw.Src)
	assert.True(t, isMostlyBlank(enc(white)), "an all-white frame is blank")

	// White with a real content block -> not blank.
	withContent := stdimage.NewRGBA(stdimage.Rect(0, 0, 100, 100))
	draw.Draw(withContent, withContent.Bounds(), stdimage.NewUniform(color.White), stdimage.Point{}, draw.Src)
	draw.Draw(withContent, stdimage.Rect(10, 10, 60, 60), stdimage.NewUniform(color.RGBA{20, 40, 80, 255}), stdimage.Point{}, draw.Src)
	assert.False(t, isMostlyBlank(enc(withContent)), "a frame with a content block is not blank")

	// Undecodable bytes -> treated as NOT blank (never waits out the budget on a quirk).
	assert.False(t, isMostlyBlank([]byte("not a png")), "undecodable is treated as non-blank")

	// A SPARSE page -- a heading and a couple of lines of thin text on white, which
	// is what most small apps are -- must not read as blank. Calling it blank
	// fails the shot, so a real page would lose its preview entirely.
	// Text is THIN STROKES, not solid blocks: a heading and two short lines of
	// real glyphs cover a fraction of a percent of a 640x400 page. That is what
	// most small apps look like (hostit's own connections demo is exactly this),
	// and it must not read as blank -- a blank verdict fails the shot, so a real
	// page would lose its preview entirely.
	sparse := stdimage.NewRGBA(stdimage.Rect(0, 0, 640, 400))
	draw.Draw(sparse, sparse.Bounds(), stdimage.NewUniform(color.White), stdimage.Point{}, draw.Src)
	ink := stdimage.NewUniform(color.RGBA{20, 20, 20, 255})
	for _, line := range []struct{ y, x0, x1, h int }{{40, 40, 250, 3}, {74, 40, 320, 2}, {96, 40, 190, 2}} {
		for x := line.x0; x < line.x1; x += 8 { // a stroke every 8px, like glyph stems
			draw.Draw(sparse, stdimage.Rect(x, line.y, x+1, line.y+line.h*4), ink, stdimage.Point{}, draw.Src)
		}
	}
	assert.False(t, isMostlyBlank(enc(sparse)), "a sparse text page is a real page, not a blank one")
}

// The cases frame-polling cannot answer, which chrome's own lifecycle events
// can: a page that never holds still, and a page that legitimately paints almost
// nothing. Both are shot as soon as the browser reports content painted.
func TestCaptureUsesChromeLifecycleSignals(t *testing.T) {
	enc := func(img stdimage.Image) []byte {
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		return buf.Bytes()
	}
	frame := func(fill color.Color, ink bool) []byte {
		im := stdimage.NewRGBA(stdimage.Rect(0, 0, 60, 60))
		draw.Draw(im, im.Bounds(), stdimage.NewUniform(fill), stdimage.Point{}, draw.Src)
		if ink {
			draw.Draw(im, stdimage.Rect(5, 5, 40, 40), stdimage.NewUniform(color.RGBA{9, 9, 9, 255}), stdimage.Point{}, draw.Src)
		}
		return enc(im)
	}

	// A MOVING page: every frame differs, so the poll would burn the whole budget.
	// With a paint signal it is shot promptly.
	moving := newFakeChrome(t, true)
	moving.lifecycle = []string{"firstContentfulPaint", "networkIdle"}
	moving.frames = [][]byte{frame(color.White, true), frame(color.RGBA{250, 250, 250, 255}, true), frame(color.RGBA{245, 245, 245, 255}, true)}
	start := time.Now()
	got, err := capture(context.Background(), moving.server.URL, "https://game/", 30*time.Second, nil)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.Less(t, time.Since(start), 25*time.Second, "a painted page is not made to wait out the settle budget")

	// A page that paints almost nothing is still a REAL page once chrome says it
	// painted: it must be stored, not rejected as blank.
	pale := newFakeChrome(t, true)
	pale.lifecycle = []string{"firstContentfulPaint", "networkIdle"}
	pale.frames = [][]byte{frame(color.White, false)}
	shot, err := capture(context.Background(), pale.server.URL, "https://sparse/", 30*time.Second, nil)
	require.NoError(t, err, "a painted-but-pale page keeps its preview")
	require.NotEmpty(t, shot)
}

// TestCaptureWaitsForStableNonBlankFrame is the core of the settle change: the
// shot must skip blank frames and return the first frame that both matches the
// prior one (page stopped changing) and is non-blank.
func TestCaptureWaitsForStableNonBlankFrame(t *testing.T) {
	enc := func(img stdimage.Image) []byte {
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		return buf.Bytes()
	}
	white := stdimage.NewRGBA(stdimage.Rect(0, 0, 60, 60))
	draw.Draw(white, white.Bounds(), stdimage.NewUniform(color.White), stdimage.Point{}, draw.Src)
	content := stdimage.NewRGBA(stdimage.Rect(0, 0, 60, 60))
	draw.Draw(content, content.Bounds(), stdimage.NewUniform(color.White), stdimage.Point{}, draw.Src)
	draw.Draw(content, stdimage.Rect(5, 5, 40, 40), stdimage.NewUniform(color.RGBA{10, 20, 30, 255}), stdimage.Point{}, draw.Src)
	W, C := enc(white), enc(content)

	// blank, then painting, then stable-and-painted: must return the content frame.
	f := newFakeChrome(t, true)
	f.frames = [][]byte{W, W, C, C}
	got, err := capture(context.Background(), f.server.URL, "https://x/", 5*time.Second, nil)
	require.NoError(t, err)
	require.Equal(t, C, got, "returns the stable non-blank frame, not a blank one")

	// A page that keeps changing yields its latest frame at the budget.
	f2 := newFakeChrome(t, true)
	f2.frames = [][]byte{C, W, C, W, C} // never two-in-a-row equal
	got2, err := capture(context.Background(), f2.server.URL, "https://x/", 400*time.Millisecond, nil)
	require.NoError(t, err)
	require.NotEmpty(t, got2, "a never-stable page still returns its latest frame")
}

// A page that merely PAUSES mid-render reads as settled (two identical frames)
// and used to be stored half-rendered. After the settle the shot waits a flat
// grace and keeps the LATER frame, so the finished page is what gets stored.
func TestCaptureTakesTheFrameAfterTheGrace(t *testing.T) {
	enc := func(img stdimage.Image) []byte {
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		return buf.Bytes()
	}
	base := stdimage.NewRGBA(stdimage.Rect(0, 0, 60, 60))
	draw.Draw(base, base.Bounds(), stdimage.NewUniform(color.White), stdimage.Point{}, draw.Src)
	half := stdimage.NewRGBA(stdimage.Rect(0, 0, 60, 60))
	draw.Draw(half, half.Bounds(), stdimage.NewUniform(color.White), stdimage.Point{}, draw.Src)
	draw.Draw(half, stdimage.Rect(5, 5, 20, 20), stdimage.NewUniform(color.RGBA{10, 20, 30, 255}), stdimage.Point{}, draw.Src)
	full := stdimage.NewRGBA(stdimage.Rect(0, 0, 60, 60))
	draw.Draw(full, full.Bounds(), stdimage.NewUniform(color.White), stdimage.Point{}, draw.Src)
	draw.Draw(full, stdimage.Rect(5, 5, 55, 55), stdimage.NewUniform(color.RGBA{10, 20, 30, 255}), stdimage.Point{}, draw.Src)
	H, F := enc(half), enc(full)

	// Two identical HALF frames look settled; the grace capture sees the FULL one.
	f := newFakeChrome(t, true)
	f.frames = [][]byte{H, H, F}
	got, err := capture(context.Background(), f.server.URL, "https://x/", 5*time.Second, nil)
	require.NoError(t, err)
	assert.Equal(t, F, got, "stores the frame taken after the grace, not the paused one")
}

// A page that is STILL BLANK when the settle budget runs out must fail rather
// than hand back the white frame: control keeps the last good card on an error,
// where storing the blank one replaces a good preview with a white box -- the
// "screenshots are sometimes white" bug.
func TestCaptureFailsRatherThanReturnABlankFrame(t *testing.T) {
	var buf bytes.Buffer
	white := stdimage.NewRGBA(stdimage.Rect(0, 0, 60, 60))
	draw.Draw(white, white.Bounds(), stdimage.NewUniform(color.White), stdimage.Point{}, draw.Src)
	require.NoError(t, png.Encode(&buf, white))
	W := buf.Bytes()

	f := newFakeChrome(t, true)
	f.frames = [][]byte{W, W, W, W} // never paints anything
	_, err := capture(context.Background(), f.server.URL, "https://x/", 400*time.Millisecond, nil)
	require.Error(t, err, "a blank-at-the-end shot is an error, not a white card")
	assert.Contains(t, err.Error(), "blank")
}
