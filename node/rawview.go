package node

import "path/filepath"

// RawAppsViewDir returns where the daemon's raw (idmap-free) view of every app's
// files is bind-mounted, derived from the app-socket path.
//
// It sits at the run root -- a SIBLING of the socket's subdir, not next to the
// socket itself. The socket lives in its own subdir (e.g. /run/hostit/app) so
// that only that subdir is mounted into containers; placing the raw view beside
// the socket would drop it back inside that mount and re-expose every app's
// files. One level up (e.g. /run/hostit/node/apps-raw) keeps it out of reach.
func RawAppsViewDir(socketFile string) string {
	runRoot := filepath.Dir(filepath.Dir(socketFile))
	return filepath.Join(runRoot, "apps-raw")
}
