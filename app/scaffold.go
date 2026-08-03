package app

import (
	"fmt"

	"heckel.io/hostit/store"
)

const (
	// scaffoldHostitYml is the commented hostit.yml template placed in new app homes
	scaffoldHostitYml = `# hostit app config -- deploy with "hostit up", see README.txt
#
# Pick ONE of the two modes below.
#
# --- Workspace mode: run a command in your workspace container ---
# The command MUST listen on 0.0.0.0:$PORT ($PORT is set for you).
#
# run: python3 -m http.server $PORT
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
	// scaffoldReadme is the README.txt template placed in new app homes; the
	// placeholders are app URL, port, port again, and app name
	scaffoldReadme = `Welcome to your hostit app!

Your app will be served at:  %s
Your assigned port:          %d

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

    hostit up        # (re)deploy and start
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
)

// scaffoldFiles returns the initial files for a new app's home directory; existing
// files are never overwritten by SystemOps.WriteScaffold
func (m *Manager) scaffoldFiles(name string, port int) map[string]string {
	url := m.URL(&store.App{Name: name, Port: port})
	return map[string]string{
		"hostit.yml": scaffoldHostitYml,
		"README.txt": fmt.Sprintf(scaffoldReadme, url, port, port, name),
	}
}
