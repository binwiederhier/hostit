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
	"sync"

	"github.com/urfave/cli/v2"
)

const (
	// A placeholder is ephemeral -- replaced the moment the owner builds something
	// -- so its chat is small, bounded and in-memory, never persisted.
	maxChatName     = 40
	maxChatText     = 280
	maxChatMessages = 100
	maxChatBody     = 4096
)

var cmdPlaceholder = &cli.Command{
	Name:  "placeholder",
	Usage: "Serve hostit's built-in placeholder app (a new app runs this until it is built)",
	Flags: []cli.Flag{
		&cli.IntFlag{Name: "port", Aliases: []string{"p"}, Usage: "port to listen on; defaults to $PORT"},
	},
	Action: execPlaceholder,
}

// chatMessage is one line in the placeholder's chat
type chatMessage struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// chatRoom is a bounded in-memory message log. It exists only to make the
// placeholder feel alive and to show a real backend is running; nothing here
// survives a restart, and it holds at most maxChatMessages.
type chatRoom struct {
	msgs []chatMessage
	mu   sync.Mutex // Protects msgs
}

// post validates and appends a message, returning false for an empty one. Name
// and text are trimmed to their limits so one visitor cannot flood memory.
func (r *chatRoom) post(name, text string) (chatMessage, bool) {
	name, text = strings.TrimSpace(name), strings.TrimSpace(text)
	if text == "" {
		return chatMessage{}, false
	}
	if name == "" {
		name = "anon"
	}
	msg := chatMessage{Name: truncate(name, maxChatName), Text: truncate(text, maxChatText)}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, msg)
	if len(r.msgs) > maxChatMessages {
		r.msgs = r.msgs[len(r.msgs)-maxChatMessages:]
	}
	return msg, true
}

func (r *chatRoom) list() []chatMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]chatMessage, len(r.msgs))
	copy(out, r.msgs)
	return out
}

// truncate caps a string to n runes (not bytes, so it never splits a character)
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// placeholderHandler serves the placeholder page and its chat. The page renders
// messages client-side with textContent, so a message is never interpreted as
// HTML: the chat is public and unauthenticated, and this is what keeps it safe.
func placeholderHandler() http.Handler {
	room := &chatRoom{}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeChatJSON(w, room.list())
		case http.MethodPost:
			var m chatMessage
			if err := json.NewDecoder(io.LimitReader(r.Body, maxChatBody)).Decode(&m); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			msg, ok := room.post(m.Name, m.Text)
			if !ok {
				http.Error(w, "message cannot be empty", http.StatusBadRequest)
				return
			}
			writeChatJSON(w, msg)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, placeholderPage)
	})
	return mux
}

func writeChatJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
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
  .log { margin: 18px 0 0; height: 200px; overflow-y: auto; padding: 12px; background: var(--bg);
         border: 1px solid var(--line); border-radius: 10px; font-size: 14px; }
  .msg { margin: 0 0 6px; overflow-wrap: anywhere; }
  .name { font-weight: 600; }
  .empty { color: var(--muted); }
  form { display: flex; gap: 8px; margin-top: 10px; }
  input { font: inherit; padding: 8px 10px; border: 1px solid var(--line); border-radius: 8px;
          background: var(--bg); color: var(--fg); min-width: 0; }
  #name { width: 92px; flex: none; }
  #text { flex: 1; }
  button { font: inherit; font-weight: 500; padding: 8px 14px; border: 0; border-radius: 8px;
           background: var(--accent); color: #fff; cursor: pointer; }
  .foot { margin-top: 16px; padding-top: 14px; border-top: 1px solid var(--line); font-size: 12px; color: var(--muted); }
</style>
<div class="card">
  <h1><span class="dot"></span>This is a placeholder app</h1>
  <p>Nothing has been built here yet. The owner will replace it with a real app.</p>
  <p>In the meantime, say hi to whoever else is passing through:</p>
  <div id="log" class="log"><span class="empty">No messages yet. Be the first.</span></div>
  <form id="f">
    <input id="name" placeholder="name" maxlength="40" autocomplete="off">
    <input id="text" placeholder="message" maxlength="280" autocomplete="off" required>
    <button type="submit">Send</button>
  </form>
  <div class="foot">Served by a small Go backend running on hostit, not a static file.</div>
</div>
<script>
  var log = document.getElementById("log");
  function render(msgs) {
    if (!msgs || !msgs.length) { return; }
    log.innerHTML = "";
    msgs.forEach(function (m) {
      var row = document.createElement("div"); row.className = "msg";
      var who = document.createElement("span"); who.className = "name"; who.textContent = m.name + ": ";
      var what = document.createElement("span"); what.textContent = m.text;
      row.appendChild(who); row.appendChild(what); log.appendChild(row);
    });
    log.scrollTop = log.scrollHeight;
  }
  function load() { fetch("/chat").then(function (r) { return r.json(); }).then(render).catch(function () {}); }
  document.getElementById("f").addEventListener("submit", function (e) {
    e.preventDefault();
    var text = document.getElementById("text");
    fetch("/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: document.getElementById("name").value, text: text.value })
    }).then(function () { text.value = ""; load(); });
  });
  load();
  setInterval(load, 3000);
</script>
`
