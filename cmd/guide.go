package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"
	"heckel.io/hostit/app"
	"heckel.io/hostit/appctl"
)

var (
	// cmdGuide prints the platform's own instructions. They used to be a
	// HOSTIT.txt scaffolded into every app, which meant a fourth copy of the
	// same text (banner, web docs, agent API) drifting in every app directory,
	// sitting next to README.md as if it were the app's own file.
	cmdGuide = &cli.Command{
		Name:   "guide",
		Usage:  "Explain how apps work on this host",
		Action: execGuide,
	}
)

func execGuide(_ *cli.Context) error {
	self, err := appctl.NewController(appctl.DefaultSocketFile()).Self()
	if err != nil {
		return fmt.Errorf("cannot identify this app: %w", err)
	}
	fmt.Print(guideText(self))
	return nil
}

// guideText is the long form: what this container is, where things go, what is
// installed, and how to get an app running
func guideText(self *appctl.SelfInfo) string {
	return "\n" +
		"  Your app \"" + self.Name + "\" is served at " + self.URL + "\n" +
		"  Its port is " + strconv.Itoa(self.Port) + ", provided to your app as $PORT.\n" +
		"\n" +
		"WHERE THINGS GO\n" +
		"  " + appctl.PublicDir + "/       files served on the web; static mode serves exactly this\n" +
		"  " + appctl.BinDir + "/          binaries and scripts you run (run: ./" + appctl.BinDir + "/myapp)\n" +
		"  " + appctl.LogDir + "/          your app's output (\"hostit logs\" reads it)\n" +
		"  " + appctl.SrcDir + "/          your source, if you keep it here\n" +
		"  " + appctl.DocsDir + "/         this app's own documentation; update it as you change things\n" +
		"  hostit.yml     how the app runs\n" +
		"  README.md      what the app is, and its worklog -- yours to write\n" +
		"\n" +
		"  Directories are created as you write into them. If your app serves files\n" +
		"  itself, point it at " + appctl.PublicDir + "/ and NEVER at this home directory: the home\n" +
		"  also holds hostit.yml and .ssh/, and serving it would publish them.\n" +
		"\n" +
		"HOW TO RUN SOMETHING\n" +
		"  Say what this app is in hostit.yml, then run \"hostit up\":\n" +
		"\n" +
		"    mode: static          hostit serves " + appctl.PublicDir + "/, nothing to run\n" +
		"    mode: app             your command serves it, via:\n" +
		"      run: ./" + appctl.BinDir + "/myapp   listening on 0.0.0.0:$PORT\n" +
		"\n" +
		"  Keep a one-line \"description:\" in hostit.yml. Your app's page shows it,\n" +
		"  and it is what the next AI session starts from.\n" +
		"\n" +
		"WHAT IS INSTALLED\n" +
		"  " + wrap(app.WorkspaceRuntimes, 74, "  ") + "\n" +
		"  You are root in here, so \"apt-get install <package>\" works for anything\n" +
		"  else. Installed packages last until the container is recreated.\n" +
		"\n" +
		"  Prefer keeping your source here: put it in " + appctl.SrcDir + "/ and add a build step,\n" +
		"  which runs before the app starts (a failed build leaves the app alone):\n" +
		"\n" +
		"    prepare: cd " + appctl.SrcDir + " && go build -o ../" + appctl.BinDir + "/myapp .\n" +
		"    run: ./" + appctl.BinDir + "/myapp\n" +
		"\n" +
		"  It builds where it runs, so no cross-compiling and no toolchain of your\n" +
		"  own. Uploading a prebuilt binary to " + appctl.BinDir + "/ works too, but then the app\n" +
		"  is only a binary and the next session has nothing to change.\n" +
		"\n" +
		"COMMANDS\n" +
		"  hostit up          apply hostit.yml and (re)start the app\n" +
		"  hostit down        stop the app\n" +
		"  hostit restart     restart it\n" +
		"  hostit status      is it running?\n" +
		"  hostit logs -f     watch its output\n" +
		"\n" +
		"  Full documentation: " + docsURL(self.URL) + "\n" +
		"\n"
}

// docsURL turns an app URL into this instance's documentation URL: the docs are
// served by the daemon on the base domain, one label up from any app
func docsURL(appURL string) string {
	rest, ok := strings.CutPrefix(appURL, "https://")
	if !ok {
		return "the hostit web app"
	}
	_, base, ok := strings.Cut(rest, ".")
	if !ok {
		return "the hostit web app"
	}
	return "https://" + base + "/docs"
}

// wrap breaks text at width, indenting continuation lines, so the guide reads
// on an 80-column terminal
func wrap(text string, width int, indent string) string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"+indent)
}
