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
# --- Process mode: run a command directly (must listen on $PORT) ---
#
# run: ./server
# env:
#   DEBUG: "true"
#
# --- Container mode: run a container image (rootless podman) ---
#
# image: docker.io/library/nginx:alpine   # or use "build: ." with a Dockerfile
# container-port: 80                      # port the app listens on INSIDE the container
# env:
#   DEBUG: "true"
# volumes:
#   - ./data:/data
`
	// scaffoldReadme is the README.txt template placed in new app homes; the three
	// placeholders are app URL, app name and assigned loopback port
	scaffoldReadme = `Welcome to your hostit app!

Your app will be served at:  %s
Your assigned port:          %d

How it works
------------
- Upload your app files to this directory (scp/rsync/sftp/git).
- Edit hostit.yml: either a "run:" command (process mode) or an "image:"/"build:"
  (container mode, rootless podman).
- Process mode: your app MUST listen on 127.0.0.1:$PORT ($PORT is set for you).
- Container mode: hostit maps your assigned port to "container-port" for you.
- Then run:

    hostit up        # (re)deploy and start
    hostit status    # show service status
    hostit logs -f   # follow logs
    hostit restart   # restart
    hostit down      # stop
    hostit info      # show app name, URL, port

Your app runs as an isolated Unix user (%s) via systemd user services; it survives
reboots and is restarted on failure.
`
)

// scaffoldFiles returns the initial files for a new app's home directory; existing
// files are never overwritten by SystemOps.WriteScaffold
func (m *Manager) scaffoldFiles(name string, port int) map[string]string {
	url := m.URL(&store.App{Name: name, Port: port})
	return map[string]string{
		"hostit.yml": scaffoldHostitYml,
		"README.txt": fmt.Sprintf(scaffoldReadme, url, port, name),
	}
}
