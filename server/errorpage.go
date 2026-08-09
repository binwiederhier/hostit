package server

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"heckel.io/hostit/config"
)

// errorPageTemplate renders the page visitors see when an app subdomain does
// not serve anything. It deliberately says little and looks the same whether the
// name is free or a registered app that is merely stopped: anything else lets an
// outsider enumerate which app names are taken. The owner learns their app is
// down from the dashboard, not from this page.
var errorPageTemplate = template.Must(template.New("errorpage").Parse(`<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
  :root { color-scheme: light dark; --fg: #14181d; --muted: #5b6672; --bg: #f6f7f9; --card: #fff; --line: #e3e7ec; --accent: #10b981; --err: #ef4444; --mark: #14181d; --markfg: #4ade80; }
  @media (prefers-color-scheme: dark) {
    :root { --fg: #e8ecf1; --muted: #97a3b0; --bg: #14181d; --card: #1b2027; --line: #2a313a; --mark: #0d1117; }
  }
  * { box-sizing: border-box; }
  body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 24px;
         background: var(--bg); color: var(--fg);
         font: 16px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; }
  .card { width: 100%; max-width: 470px; background: var(--card); border: 1px solid var(--line);
          border-radius: 16px; padding: 28px; text-align: center; }
  .eyebrow { display: inline-block; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
             font-size: 12px; font-weight: 700; letter-spacing: 0.14em; text-transform: uppercase;
             color: var(--err); border: 1px solid color-mix(in srgb, var(--err) 40%, var(--line));
             background: color-mix(in srgb, var(--err) 10%, transparent); border-radius: 999px; padding: 3px 11px; }
  h1 { margin: 14px 0 0; font-size: 23px; letter-spacing: -0.02em; text-wrap: balance; }
  p { margin: 10px 0 0; color: var(--muted); font-size: 14px; }
  .game { position: relative; height: 138px; margin: 20px 0 8px; border: 1px solid var(--line);
          border-radius: 12px; background: var(--bg); overflow: hidden; cursor: pointer; }
  .game canvas { display: block; width: 100%; height: 100%; }
  .game .tip { position: absolute; left: 0; right: 0; bottom: 9px; font-size: 12px; color: var(--muted);
               pointer-events: none; letter-spacing: 0.01em; }
  .cap { font-size: 12.5px; color: var(--muted); }
  .brand { margin-top: 22px; padding-top: 18px; border-top: 1px solid var(--line); }
  .brand a { display: inline-flex; align-items: center; gap: 9px; text-decoration: none; color: var(--fg); }
  .mark { display: inline-grid; place-items: center; width: 26px; height: 26px; border-radius: 7px;
          background: var(--mark); color: var(--markfg); font-family: ui-monospace, Menlo, Consolas, monospace;
          font-size: 14px; font-weight: 700; }
  .wm { font-weight: 650; font-size: 16px; letter-spacing: -0.01em; }
  .what { margin: 10px auto 0; max-width: 40ch; }
  .what a { color: var(--accent); text-decoration: none; font-weight: 600; }
</style>
<div class="card">
  <div class="eyebrow">Error {{.Code}}</div>
  <h1>{{.Headline}}</h1>
  <p>{{.Message}}</p>
  <div class="game" id="game">
    <canvas id="c"></canvas>
    <div class="tip" id="tip">Press Space or tap to jump</div>
  </div>
  <p class="cap">While you decide what to build, help this container clear its deploy pipeline.</p>
  <div class="brand">
    <a href="{{.Home}}"><span class="mark">&gt;_</span><span class="wm">hostit</span></a>
    <p class="what">hostit hosts small web apps, each in its own container with a subdomain, HTTPS and SSH. Build one and this page becomes your app. <a href="{{.Home}}">Start one &rarr;</a></p>
  </div>
</div>
<script>
  var cvs = document.getElementById("c"), ctx = cvs.getContext("2d"), tip = document.getElementById("tip");
  var css = getComputedStyle(document.documentElement);
  var col = function (n) { return css.getPropertyValue(n).trim() || "#888"; };
  var W = 0, H = 0, ground = 0, dpr = Math.max(1, Math.min(2, window.devicePixelRatio || 1));

  function resize() {
    W = cvs.clientWidth; H = cvs.clientHeight; ground = H - 26;
    cvs.width = Math.round(W * dpr); cvs.height = Math.round(H * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  }
  window.addEventListener("resize", resize);

  var SIZE = 20, GRAV = 0.6, JUMP = -9.2;
  var state = "idle", py = 0, vy = 0, obs = [], speed = 3.2, score = 0, best = 0, tick = 0;

  function reset() {
    py = ground - SIZE; vy = 0; obs = []; speed = 3.2; score = 0; tick = 0;
    for (var i = 0; i < 2; i++) obs.push({ x: W + 160 + i * 220, w: 12 + Math.random() * 12, h: 18 + Math.random() * 18 });
  }
  function start() { reset(); state = "play"; tip.style.opacity = "0"; }
  function jump() {
    if (state === "idle" || state === "over") { start(); return; }
    if (py + SIZE >= ground - 0.5) vy = JUMP;
  }

  function step() {
    tick++;
    if (tick % 240 === 0) speed += 0.25;
    vy += GRAV; py += vy;
    if (py + SIZE > ground) { py = ground - SIZE; vy = 0; }
    for (var i = 0; i < obs.length; i++) {
      var o = obs[i]; o.x -= speed;
      if (o.x + o.w < 0) { o.x = W + 120 + Math.random() * 180; o.w = 12 + Math.random() * 12; o.h = 18 + Math.random() * 18; score++; }
      var px = 30;
      if (px + SIZE > o.x && px < o.x + o.w && py + SIZE > ground - o.h) {
        best = Math.max(best, score); state = "over";
        tip.textContent = "Game over. Score " + score + ". Space to retry"; tip.style.opacity = "1";
      }
    }
  }

  function roundRect(x, y, w, h, r) {
    ctx.beginPath();
    ctx.moveTo(x + r, y); ctx.arcTo(x + w, y, x + w, y + h, r); ctx.arcTo(x + w, y + h, x, y + h, r);
    ctx.arcTo(x, y + h, x, y, r); ctx.arcTo(x, y, x + w, y, r); ctx.closePath();
  }
  function draw() {
    ctx.clearRect(0, 0, W, H);
    // Dotted ground line.
    ctx.strokeStyle = col("--line"); ctx.lineWidth = 2; ctx.setLineDash([3, 5]);
    ctx.beginPath(); ctx.moveTo(0, ground + 1); ctx.lineTo(W, ground + 1); ctx.stroke();
    ctx.setLineDash([]);
    // Obstacles (little servers to hurdle).
    ctx.fillStyle = col("--muted");
    for (var i = 0; i < obs.length; i++) { var o = obs[i]; roundRect(o.x, ground - o.h, o.w, o.h, 2); ctx.fill(); }
    // The player is the hostit cube: a dark rounded tile with a green ">_" mark, so
    // the thing you jump is the logo. A tiny hop-shadow sells the motion.
    var run = state === "play";
    var bob = run ? (Math.floor(tick / 6) % 2 === 0 ? 0 : 1) : 0;
    ctx.fillStyle = col("--mark"); roundRect(30, py + bob, SIZE, SIZE, 5); ctx.fill();
    ctx.fillStyle = col("--markfg");
    ctx.font = "700 12px ui-monospace, SFMono-Regular, Menlo, monospace";
    ctx.textAlign = "center"; ctx.textBaseline = "middle";
    ctx.fillText(">_", 30 + SIZE / 2, py + bob + SIZE / 2 + 1);
    ctx.textAlign = "left"; ctx.textBaseline = "alphabetic";
    // Score.
    if (state !== "over") {
      ctx.fillStyle = col("--muted"); ctx.font = "600 12px ui-monospace, monospace"; ctx.textAlign = "right";
      ctx.fillText(String(score).padStart(3, "0"), W - 10, 18); ctx.textAlign = "left";
    }
  }

  function loop() { if (state === "play") step(); draw(); requestAnimationFrame(loop); }

  function press(e) { if (e.type === "keydown" && e.code !== "Space" && e.code !== "ArrowUp") return; e.preventDefault(); jump(); }
  window.addEventListener("keydown", press);
  document.getElementById("game").addEventListener("pointerdown", function (e) { e.preventDefault(); jump(); });

  resize(); py = ground - SIZE; loop();
</script>
`))

// errorPageData is the template input. Title/Headline/Message come from the
// caller; Code and Home are filled in by writeErrorPage.
type errorPageData struct {
	Title    string
	Headline string
	Message  string
	Code     string // HTTP status, shown as the "Error 404" eyebrow
	Home     string // the dashboard URL, for the footer logo/link
}

// writeNothingHerePage is served both for a hostname that belongs to no app and
// for an app that exists but is not answering. The two cases are deliberately
// identical, so a visitor cannot tell a free name from a stopped app.
func (s *Server) writeNothingHerePage(w http.ResponseWriter) {
	s.writeErrorPage(w, http.StatusNotFound, &errorPageData{
		Title:    "404 - nothing deployed here",
		Headline: "Nothing's deployed here",
		Message:  "No app answers at this address. It was never built, or it wandered off. Either way, the spot is yours for the taking.",
	})
}

func (s *Server) writeErrorPage(w http.ResponseWriter, status int, data *errorPageData) {
	data.Code = strconv.Itoa(status)
	scheme := "https"
	if s.config.TLS == config.TLSOff {
		scheme = "http"
	}
	data.Home = scheme + "://" + s.config.APIHostname()
	var b strings.Builder
	if err := errorPageTemplate.Execute(&b, data); err != nil {
		http.Error(w, data.Headline, status) // Fall back to plain text
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(b.String()))
}
