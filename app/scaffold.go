package app

import (
	"fmt"
	"heckel.io/hostit/appctl"

	"heckel.io/hostit/store"
)

const (
	// scaffoldHostitYml is the hostit.yml new apps start with: a working static
	// stub, with the other modes documented right below it
	scaffoldHostitYml = `# hostit app config -- apply changes with "hostit up", see HOSTIT.txt.
#
# This is the stub app: it serves this directory as static files. Replace it.

static: public

# --- Description: one line saying what this app is ---
# Uncomment and keep it current. The owner's web page shows it, and it is what
# the next AI session starts from instead of a blank page.
#
# description: A tiny app that does X
#
# --- Static mode (above): hostit serves public/ ---
# Nothing else needed; good for plain HTML, or the built output of any frontend.
# The directory served is always public/, whatever this value says.
#
# --- Run mode: hostit runs your command in the workspace container ---
# The command MUST listen on 0.0.0.0:$PORT ($PORT is set for you).
#
# run: ./bin/server
# env:
#   DEBUG: "true"
#
# --- Image mode: your own container image ---
#
# image: docker.io/library/nginx:alpine   # or use "build: ." with a Dockerfile
# container-port: 80                      # port the app listens on INSIDE the container
# volumes:
#   - ./data:/data
`

	// scaffoldHostitTxt is the platform's own instructions; placeholders are the
	// app URL, port, runtimes, port again, and the app name
	scaffoldHostitTxt = `Welcome to your hostit app!

Your app is served at:  %s
Your assigned port:     %d

RIGHT NOW THIS IS A STUB: the placeholder page in public/index.html is being
served as static files (see "static:" in hostit.yml). Replace it with your app.

Where things go
---------------
  public/   files served on the web -- static mode serves exactly this directory
  bin/      binaries and scripts your app runs (run: ./bin/myapp)
  log/      your app's output, written by hostit ("hostit logs" reads it)
  src/      your source, if you keep it here
  hostit.yml, README.md at the top

Directories are created as you write into them.

What is installed
-----------------
The workspace container ships %s.
Anything else: "apt-get update && apt-get install -y <package>" (you are root in
here; installed packages last until the container is recreated).

Suggested stack
---------------
A single Go binary that embeds its frontend is the easiest thing to run here:
one file, no runtime to install, starts instantly.

    //go:embed all:web/dist
    var site embed.FS      // serve this with http.FileServer

    hostit.yml:  run: ./bin/myapp     # listening on 0.0.0.0:$PORT

You can build it either way: compile it wherever you work and upload the binary
to bin/, or put the source in src/ and compile it here (the Go toolchain is
installed). Compiling here is the safer default if your machine cannot produce a
linux/amd64 binary; uploading a prebuilt one is faster.

    cd src && go build -o ../bin/myapp .        # ...or, from elsewhere:
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o myapp .

Python, Node and PHP work just as well if you prefer them, and plain HTML needs
nothing at all (keep "static:").

How it works
------------
- SSH puts you INSIDE your app's container: you are root in here, and you cannot
  see other apps. Your files live in this home directory, which is the same
  directory scp/rsync/sftp write to.
- Static mode always serves public/, whatever "static:" says.
- Edit hostit.yml to pick a mode: "static:" (serve files), "run:" (your command,
  must listen on 0.0.0.0:$PORT, PORT=%d) or "image:"/"build:" (your own image).
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
- Changing image:/build:/env:/volumes: recreates the container, which also kicks
  active SSH sessions. Changing "static:" or "run:" only restarts the process.
- An AI agent can do all of this through the API instead; see the app's page in
  the hostit web app for a prompt you can paste into it.
`

	// scaffoldAppReadme is the app's OWN readme: agents read it to learn what the
	// app is and write back what they changed, and the owner sees it in the web
	// app. Placeholders are the app name, its URL and the runtimes.
	scaffoldAppReadme = `# %s

**This is a stub.** hostit created this app with a placeholder page; nothing has
been built here yet.

- URL: %s
- Currently: ` + "`static: \".\"`" + ` in hostit.yml, serving index.html as static files
- Available in the container: %s (install anything else with apt-get)
- Suggested stack: a single Go binary with an embedded frontend -- one file to
  upload, no runtime to install. Python, Node, PHP and plain HTML work too.

This file is the app's description and worklog: whatever is written here is what
hostit shows the owner, and what the next agent session reads first. Replace it
with what this app actually is, and keep it updated as you change things.
`

	// demoPageTemplate is the stub page a new app serves right away; the
	// placeholders are the app name (twice), the runtimes, the app name and the
	// SSH host
	demoPageTemplate = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s (stub)</title>
<style>
  :root { color-scheme: light dark; --fg: #14181d; --muted: #5b6672; --bg: #f6f7f9; --card: #fff; --line: #e3e7ec; --accent: #10b981; }
  @media (prefers-color-scheme: dark) {
    :root { --fg: #e8ecf1; --muted: #97a3b0; --bg: #14181d; --card: #1b2027; --line: #2a313a; }
  }
  * { box-sizing: border-box; }
  body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 24px;
         background: var(--bg); color: var(--fg);
         font: 16px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; }
  .card { width: 100%%; max-width: 620px; background: var(--card); border: 1px solid var(--line);
          border-radius: 14px; padding: 32px; }
  h1 { margin: 0 0 4px; font-size: 22px; letter-spacing: -0.01em; }
  h1 .dot { display: inline-block; width: 9px; height: 9px; border-radius: 50%%;
            background: var(--accent); margin-right: 9px; vertical-align: middle; }
  .tag { display: inline-block; margin-left: 8px; padding: 1px 8px; border-radius: 999px;
         background: var(--bg); border: 1px solid var(--line); font-size: 12px; color: var(--muted);
         vertical-align: middle; letter-spacing: 0.02em; }
  p { margin: 12px 0 0; color: var(--muted); }
  h2 { margin: 24px 0 0; font-size: 14px; letter-spacing: 0.03em; text-transform: uppercase; color: var(--muted); }
  code, pre { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 13.5px; }
  pre { margin: 10px 0 0; padding: 14px 16px; background: var(--bg); border: 1px solid var(--line);
        border-radius: 10px; overflow-x: auto; }
  .foot { margin-top: 22px; padding-top: 16px; border-top: 1px solid var(--line); font-size: 13px; color: var(--muted); }
</style>
<div class="card">
  <h1><span class="dot"></span>%s<span class="tag">stub</span></h1>
  <p>Your app is running, but nothing has been built here yet. This placeholder
     page is served as static files (<code>static: "."</code> in hostit.yml).</p>

  <h2>What is installed</h2>
  <p>%s. Install anything else with <code>apt-get install</code> -- you are root
     inside your container.</p>

  <h2>Suggested stack</h2>
  <p>A single Go binary with an embedded frontend: one file to upload, no runtime
     to install. Python, Node and PHP work too, and plain HTML needs nothing.</p>

  <h2>Replace it</h2>
  <pre>ssh %s@%s
# put your files here, edit hostit.yml, then:
hostit up</pre>
  <p>Or let an AI agent do it: your app's page in hostit has a prompt to paste.
     Details are in <code>HOSTIT.txt</code> in this directory.</p>
  <div class="foot">Served by hostit</div>
</div>
`
)

// scaffoldFiles returns the initial files for a new app's home directory; existing
// files are never overwritten by SystemOps.WriteScaffold
func (m *Manager) scaffoldFiles(name string, port int) map[string]string {
	url := m.URL(&store.App{Name: name, Port: port})
	return map[string]string{
		"hostit.yml":                     scaffoldHostitYml,
		"HOSTIT.txt":                     fmt.Sprintf(scaffoldHostitTxt, url, port, WorkspaceRuntimes, port, name),
		"README.md":                      fmt.Sprintf(scaffoldAppReadme, name, url, WorkspaceRuntimes),
		appctl.PublicDir + "/index.html": demoPage(name, m.config.SSHHostname()),
	}
}

// demoPage renders the stub page a fresh app serves; the CSS percentages in the
// template are escaped as %% and unescaped by Sprintf
func demoPage(name, sshHost string) string {
	return fmt.Sprintf(demoPageTemplate, name, name, WorkspaceRuntimes, name, sshHost)
}
