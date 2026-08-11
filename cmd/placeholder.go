package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/urfave/cli/v2"
)

const (
	// liveTick is how often the placeholder pushes fresh stats to a viewer. Every
	// tick doubles as the SSE keepalive, so an idle stream never times out.
	liveTick = 1 * time.Second
	// visitorToken is replaced per request with the loading visitor's number, so the
	// page is server-rendered proof that a real backend handled the request.
	visitorToken = "__VISITOR__"
)

var cmdPlaceholder = &cli.Command{
	Name:  "placeholder",
	Usage: "Serve hostit's built-in placeholder app (a new app runs this until it is built)",
	Flags: []cli.Flag{
		&cli.IntFlag{Name: "port", Aliases: []string{"p"}, Usage: "port to listen on; defaults to $PORT"},
	},
	Action: execPlaceholder,
}

// liveStat is one snapshot the placeholder pushes to viewers, showing the page is
// backed by a running process rather than a static file.
type liveStat struct {
	Time    string `json:"time"`    // server clock, HH:MM:SS
	Uptime  string `json:"uptime"`  // how long this server has been running
	Visits  int64  `json:"visits"`  // page loads since it started
	Viewers int64  `json:"viewers"` // how many are watching the live stream right now
}

// placeholderStats is the placeholder's only state: a start time and two counters.
// Everything is in memory and resets on restart, which is fine -- a placeholder is
// replaced the moment its owner builds something.
type placeholderStats struct {
	started time.Time
	visits  atomic.Int64
	viewers atomic.Int64
}

func (s *placeholderStats) snapshot() liveStat {
	return liveStat{
		Time:    time.Now().Format("15:04:05"),
		Uptime:  formatUptime(time.Since(s.started)),
		Visits:  s.visits.Load(),
		Viewers: s.viewers.Load(),
	}
}

// formatUptime renders a duration compactly, e.g. "12s", "3m 12s", "1h 03m".
func formatUptime(d time.Duration) string {
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh %02dm", s/3600, (s%3600)/60)
	}
}

// placeholderHandler serves the placeholder page plus a live stats stream. The page
// is a running Go server: it renders the loading visitor's number server-side and
// pushes a ticking clock, uptime and counters over SSE, so a brand-new app visibly
// proves it can execute code.
func placeholderHandler() http.Handler {
	stats := &placeholderStats{started: time.Now()}
	mux := http.NewServeMux()
	// Server-sent events: the browser opens this once and the backend pushes a fresh
	// snapshot every tick (which doubles as the keepalive), so the clock ticks and
	// the counters move without polling.
	mux.HandleFunc("/live", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		stats.viewers.Add(1)
		defer stats.viewers.Add(-1)
		writeStatEvent(w, stats.snapshot()) // fill the page at once
		flusher.Flush()
		ticker := time.NewTicker(liveTick)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				writeStatEvent(w, stats.snapshot())
				flusher.Flush()
			}
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		n := stats.visits.Add(1)
		page := strings.Replace(placeholderPage, visitorToken, strconv.FormatInt(n, 10), 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, page)
	})
	return mux
}

// writeStatEvent writes one stats snapshot as an SSE "data:" frame
func writeStatEvent(w http.ResponseWriter, s liveStat) {
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}

func execPlaceholder(c *cli.Context) error {
	port := c.Int("port")
	if port == 0 {
		port, _ = strconv.Atoi(os.Getenv("PORT"))
	}
	if port == 0 {
		return errors.New("no port: pass --port or set $PORT")
	}
	addr := fmt.Sprintf("0.0.0.0:%d", port)
	fmt.Printf("Placeholder app serving on %s\n", addr)
	server := &http.Server{
		Addr:              addr,
		Handler:           placeholderHandler(),
		ReadHeaderTimeout: staticReadHeaderTimeout,
	}
	return server.ListenAndServe()
}

// placeholderPage is the whole placeholder frontend: a small page saying nothing
// is built yet, plus a chat backed by /chat. It is deliberately a running Go
// server rather than a static file, so a new app proves it can execute code.
const placeholderPage = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Placeholder app</title>
<style>
  :root { color-scheme: light dark; --fg: #14181d; --muted: #5b6672; --bg: #f6f7f9; --card: #fff; --line: #e3e7ec; --accent: #10b981; }
  @media (prefers-color-scheme: dark) {
    :root { --fg: #e8ecf1; --muted: #97a3b0; --bg: #14181d; --card: #1b2027; --line: #2a313a; }
  }
  * { box-sizing: border-box; }
  body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 24px;
         background: var(--bg); color: var(--fg);
         font: 16px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; }
  .card { width: 100%; max-width: 480px; background: var(--card); border: 1px solid var(--line);
          border-radius: 14px; padding: 28px; }
  h1 { margin: 0; font-size: 20px; letter-spacing: -0.01em; }
  h1 .dot { display: inline-block; width: 9px; height: 9px; border-radius: 50%;
            background: var(--accent); margin-right: 9px; vertical-align: middle; }
  p { margin: 10px 0 0; color: var(--muted); font-size: 14px; }
  .clock { margin: 20px 0 4px; text-align: center; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
           font-size: 42px; font-weight: 700; letter-spacing: 0.02em; font-variant-numeric: tabular-nums; color: var(--accent); }
  .clock small { display: block; font-size: 11px; font-weight: 600; letter-spacing: 0.09em;
                 text-transform: uppercase; color: var(--muted); margin-top: 2px; }
  .stats { display: flex; gap: 10px; margin-top: 16px; }
  .stat { flex: 1; background: var(--bg); border: 1px solid var(--line); border-radius: 10px; padding: 10px 6px; text-align: center; }
  .stat .k { font-size: 10.5px; text-transform: uppercase; letter-spacing: 0.05em; color: var(--muted); }
  .stat .v { font-size: 21px; font-weight: 700; font-variant-numeric: tabular-nums; margin-top: 2px; }
  .you { margin-top: 14px; font-size: 13px; }
  .you b { font-variant-numeric: tabular-nums; }
  .foot { margin-top: 16px; padding-top: 14px; border-top: 1px solid var(--line); font-size: 12px; color: var(--muted); }
</style>
<div class="card">
  <h1><span class="dot"></span>This is a placeholder app</h1>
  <p>Nothing's built here yet, but this is a live Go server, not a static file. Everything below is computed on the server, right now:</p>
  <div class="clock"><span id="clocktime">--:--:--</span><small>server time</small></div>
  <div class="stats">
    <div class="stat"><div class="k">Uptime</div><div class="v" id="uptime">--</div></div>
    <div class="stat"><div class="k">Visits</div><div class="v" id="visits">--</div></div>
    <div class="stat"><div class="k">Watching now</div><div class="v" id="viewers">--</div></div>
  </div>
  <p class="you">You are visitor <b>#__VISITOR__</b> since this server started.</p>
  <div class="foot">Served by a small Go backend on hostit. Its owner will replace it with a real app.</div>
</div>
<script>
  function set(id, v) { document.getElementById(id).textContent = v; }
  // Server-sent events: the backend pushes a fresh snapshot every second, so the
  // clock ticks and the counters move with no polling. EventSource reconnects itself.
  var stream = new EventSource("/live");
  stream.onmessage = function (e) {
    try {
      var s = JSON.parse(e.data);
      set("clocktime", s.time);
      set("uptime", s.uptime);
      set("visits", s.visits);
      set("viewers", s.viewers);
    } catch (_) {}
  };
</script>
`
