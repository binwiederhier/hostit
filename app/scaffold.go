package app

import (
	"fmt"

	"heckel.io/hostit/store"
)

const (
	// scaffoldHostitYml is the hostit.yml new apps start with: a working stub that
	// runs hostit's own placeholder backend, with the other modes documented below.
	scaffoldHostitYml = `# hostit app config -- apply changes with "hostit up"; see "hostit guide".
#
# This is the stub app: it runs hostit's built-in placeholder, a small Go
# backend. Replace "run:" with your own command, or switch to "mode: static".

mode: app
run: hostit placeholder

# --- Description: one line saying what this app is ---
# Uncomment and keep it current. The owner's web page shows it, and it is what
# the next AI session starts from instead of a blank page.
#
# description: A tiny app that does X
#
# --- mode: static -- hostit serves public/ ---
# Good for plain HTML, or the built output of any frontend. Put files in public/:
#
# mode: static
#
# --- mode: app -- hostit runs your command in the workspace container ---
# The command MUST listen on 0.0.0.0:$PORT ($PORT is set for you).
#
# mode: app
# prepare: cd src && go build -o ../bin/server .
# run: ./bin/server
# env:
#   DEBUG: "true"
#
`

	// scaffoldAppReadme is the app's OWN readme: agents read it to learn what the
	// app is and write back what they changed, and the owner sees it in the web
	// app. Placeholders are the app name, its URL and the runtimes.
	scaffoldAppReadme = `# %s

**This is a stub.** hostit created this app running its built-in placeholder (a
small Go backend); nothing has been built here yet.

- URL: %s
- Currently: ` + "`mode: app`" + `, run: ` + "`hostit placeholder`" + ` in hostit.yml
- Available in the container: %s (install anything else with apt-get)
- Suggested stack: a single Go binary with an embedded frontend -- one file to
  upload, no runtime to install. Python and plain HTML (mode: static) work too.

This file is the app's description and worklog: whatever is written here is what
hostit shows the owner, and what the next agent session reads first. Replace it
with what this app actually is, and keep it updated as you change things.
`
)

// scaffoldFiles returns the initial files for a new app's home directory; existing
// files are never overwritten by SystemOps.WriteScaffold
func (m *Manager) scaffoldFiles(name string, port int) map[string]string {
	url := m.URL(&store.App{Name: name, Port: port})
	return map[string]string{
		// Silences the host's login banner (Ubuntu's MOTD prints the host's IP,
		// load and disk usage) and the "Last login" line, so an SSH session opens
		// with hostit's own greeting and nothing about the machine underneath
		".hushlogin": "",
		"hostit.yml": scaffoldHostitYml,
		"README.md":  fmt.Sprintf(scaffoldAppReadme, name, url, WorkspaceRuntimes),
	}
}
