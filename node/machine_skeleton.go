package node

import (
	_ "embed"
	"fmt"
)

// skeletonHostitYml is the hostit.yml new apps start with: a working stub that
// serves public/ (mode: static), with the other modes documented inline.
//
//go:embed skeleton/hostit.yml
var skeletonHostitYml string

// skeletonPublicIndex is the placeholder page a new app serves out of public/
// until its owner replaces it. Static, so a fresh app needs no running process.
//
//go:embed skeleton/public/index.html
var skeletonPublicIndex string

// skeletonAppReadme is the app's OWN readme: agents read it to learn what the app
// is and write back what they changed, and the owner sees it in the web app. Its
// placeholders (in order) are the app name, its URL and the container runtimes.
//
//go:embed skeleton/readme.md
var skeletonAppReadme string

// skeletonFiles returns the initial files for a new app's home directory; existing
// files are never overwritten by WriteSkeleton. It is a plain function: it needs
// only the app's name, URL and available runtimes, not the Manager.
func skeletonFiles(name, url, runtimes string) map[string]string {
	return map[string]string{
		// Silences the host's login banner (Ubuntu's MOTD prints the host's IP,
		// load and disk usage) and the "Last login" line, so an SSH session opens
		// with hostit's own greeting and nothing about the Machine underneath
		".hushlogin":        "",
		"hostit.yml":        skeletonHostitYml,
		"public/index.html": skeletonPublicIndex,
		"README.md":         fmt.Sprintf(skeletonAppReadme, name, url, runtimes),
	}
}
