package app

import (
	_ "embed"
	"fmt"
)

// scaffoldHostitYml is the hostit.yml new apps start with: a working stub that
// runs hostit's own placeholder backend, with the other modes documented inline.
//
//go:embed scaffold/hostit.yml
var scaffoldHostitYml string

// scaffoldAppReadme is the app's OWN readme: agents read it to learn what the app
// is and write back what they changed, and the owner sees it in the web app. Its
// placeholders (in order) are the app name, its URL and the container runtimes.
//
//go:embed scaffold/readme.md
var scaffoldAppReadme string

// scaffoldFiles returns the initial files for a new app's home directory; existing
// files are never overwritten by WriteScaffold. It is a plain function: it needs
// only the app's name, URL and available runtimes, not the Manager.
func scaffoldFiles(name, url, runtimes string) map[string]string {
	return map[string]string{
		// Silences the host's login banner (Ubuntu's MOTD prints the host's IP,
		// load and disk usage) and the "Last login" line, so an SSH session opens
		// with hostit's own greeting and nothing about the machine underneath
		".hushlogin": "",
		"hostit.yml": scaffoldHostitYml,
		"README.md":  fmt.Sprintf(scaffoldAppReadme, name, url, runtimes),
	}
}
