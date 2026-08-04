package app

import (
	"fmt"

	"heckel.io/hostit/store"
)

const (
	// scaffoldHostitYml is the hostit.yml new apps start with: a working demo,
	// with the alternatives documented right below it
	scaffoldHostitYml = `# hostit app config -- apply changes with "hostit up", see HOSTIT.txt.
#
# This is the demo app. Replace it with your own.

run: python3 -m http.server $PORT

# --- Workspace mode (above): run a command in your workspace container ---
# The command MUST listen on 0.0.0.0:$PORT ($PORT is set for you).
#
# run: ./server --debug
# env:
#   DEBUG: "true"
#
# --- Image mode: run a container image of your own ---
#
# image: docker.io/library/nginx:alpine   # or use "build: ." with a Dockerfile
# container-port: 80                      # port the app listens on INSIDE the container
# env:
#   DEBUG: "true"
# volumes:
#   - ./data:/data
`

	// scaffoldReadme is the README.txt template; placeholders are app URL, port,
	// port again, and app name
	scaffoldHostitTxt = `Welcome to your hostit app!

Your app is served at:  %s
Your assigned port:     %d

It is already running: the demo page in index.html is being served by the
"run:" command in hostit.yml. Replace both with your own app.

How it works
------------
- SSH puts you INSIDE your app's container (like a docker container of your
  own): you are root in here, you can apt-get install things, and you cannot
  see other apps. Your files live in this home directory, which is the same
  directory scp/rsync/sftp write to.
- Edit hostit.yml: either a "run:" command (executed in your workspace
  container; must listen on 0.0.0.0:$PORT, PORT=%d) or an "image:"/"build:"
  (your own container image; hostit maps the port for you).
- Then run:

    hostit up        # apply changes and (re)start
    hostit status    # show service status
    hostit logs -f   # follow logs
    hostit restart   # restart
    hostit down      # stop
    hostit info      # show app name, URL, port

Notes
-----
- Your app (%s) survives reboots and restarts on failure.
- Changing image:/build:/env:/volumes: recreates the container on "hostit up",
  which also kicks active SSH sessions (like docker). A changed "run:" command
  restarts only the app process.
- Need tools not in the workspace? "apt-get update && apt-get install -y ..."
  (installed packages persist until the container is recreated).
`

	// scaffoldAppReadme is the app's OWN readme: agents read it to learn what the
	// app is and write back what they changed, and the owner sees it in the web
	// app. Placeholders are the app name and its URL.
	scaffoldAppReadme = `# %s

Nothing has been built here yet. This file is the app's description and
worklog: whatever is written here is what hostit shows the owner, and what the
next agent session reads first.

- URL: %s
- Replace index.html (or point hostit.yml at your own command or image)
- Keep this file updated: what the app does, how it is built, what changed

`

	// demoPageTemplate is the placeholder page a new app serves right away; the
	// placeholder is the app name (twice)
	demoPageTemplate = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s is running</title>
<style>
  :root { color-scheme: light dark; --fg: #14181d; --muted: #5b6672; --bg: #f6f7f9; --card: #fff; --line: #e3e7ec; --accent: #10b981; }
  @media (prefers-color-scheme: dark) {
    :root { --fg: #e8ecf1; --muted: #97a3b0; --bg: #14181d; --card: #1b2027; --line: #2a313a; }
  }
  * { box-sizing: border-box; }
  body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 24px;
         background: var(--bg); color: var(--fg);
         font: 16px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; }
  .card { width: 100%%; max-width: 560px; background: var(--card); border: 1px solid var(--line);
          border-radius: 14px; padding: 32px; }
  h1 { margin: 0 0 4px; font-size: 22px; letter-spacing: -0.01em; }
  h1 .dot { display: inline-block; width: 9px; height: 9px; border-radius: 50%%;
            background: var(--accent); margin-right: 9px; vertical-align: middle; }
  p { margin: 12px 0 0; color: var(--muted); }
  code, pre { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 13.5px; }
  pre { margin: 16px 0 0; padding: 14px 16px; background: var(--bg); border: 1px solid var(--line);
        border-radius: 10px; overflow-x: auto; }
  .foot { margin-top: 22px; padding-top: 16px; border-top: 1px solid var(--line); font-size: 13px; color: var(--muted); }
</style>
<div class="card">
  <h1><span class="dot"></span>%s is running</h1>
  <p>This is the demo page hostit created for your app. Replace it with your own:</p>
  <pre>ssh %s@%s
# put your files here, edit hostit.yml, then:
hostit up</pre>
  <p>Everything you need is in <code>HOSTIT.txt</code> in your app's home directory.</p>
  <div class="foot">Served by hostit</div>
</div>
`
)

// scaffoldFiles returns the initial files for a new app's home directory; existing
// files are never overwritten by SystemOps.WriteScaffold
func (m *Manager) scaffoldFiles(name string, port int) map[string]string {
	url := m.URL(&store.App{Name: name, Port: port})
	return map[string]string{
		"hostit.yml": scaffoldHostitYml,
		"HOSTIT.txt": fmt.Sprintf(scaffoldHostitTxt, url, port, port, name),
		"README.md":  fmt.Sprintf(scaffoldAppReadme, name, url),
		"index.html": demoPage(name, m.config.SSHHostname()),
	}
}

// demoPage renders the placeholder page a fresh app serves; the CSS percentages
// in the template are escaped as %% and unescaped by Sprintf
func demoPage(name, sshHost string) string {
	return fmt.Sprintf(demoPageTemplate, name, name, name, sshHost)
}
