package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/urfave/cli/v2"

	"heckel.io/hostit/cmd/util"
	"heckel.io/hostit/control"
	"heckel.io/hostit/control/config"
	"heckel.io/hostit/system/stats"
)

// cmdStatus prints the whole cluster: who is in it, whether they are reporting,
// and what they are carrying. It reads the registry directly, like `node list`
// and `proxy list`, so it works with the daemon stopped -- which is exactly when
// an operator most wants to look.
var (
	cmdStatus = &cli.Command{
		Name:  "status",
		Usage: "Show the cluster: nodes, proxies, apps and totals",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: config.DefaultControlConfigFile, Usage: "control config file"},
			&cli.BoolFlag{Name: "json", Usage: "print the raw status as JSON"},
		},
		Action: execStatus,
	}
)

func execStatus(c *cli.Context) error {
	conf, s, err := nodeStore(c)
	if err != nil {
		return err
	}
	defer s.Close()
	status, err := control.ClusterStatus(s, conf.DataDir, time.Now())
	if err != nil {
		return err
	}
	if c.Bool("json") {
		enc := json.NewEncoder(c.App.Writer)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	}
	printStatus(c, status)
	return nil
}

// statCell renders a member's memory or disk as "used/total (pct%)", or "--"
// for a member that has never reported.
func statCell(used, total, pct int) string {
	if total <= 0 {
		return "--"
	}
	return fmt.Sprintf("%s/%s (%d%%)", humanMB(used), humanMB(total), pct)
}

// loadCell puts the load average next to the core count it is spread over,
// since 4.0 means something different on 1 core than on 8.
func loadCell(s stats.Stats) string {
	if s.CPUCount == 0 {
		return "--"
	}
	return fmt.Sprintf("%.2f / %d", s.Load1, s.CPUCount)
}

func printStatus(c *cli.Context, status *control.Status) {
	w := c.App.Writer
	if status.Control != nil {
		fmt.Fprintln(w, util.Title("CONTROL"))
		fmt.Fprintln(w, util.Render([]string{"NAME", "VERSION", "RAM", "DISK", "LOAD / CPUS"}, [][]string{{
			status.Control.Name,
			dashIfEmpty(shortVersion(status.Control.Version)),
			statCell(status.Control.Stats.MemoryUsedMB, status.Control.Stats.MemoryTotalMB, status.Control.Stats.MemoryPercent()),
			statCell(status.Control.Stats.DiskUsedMB, status.Control.Stats.DiskTotalMB, status.Control.Stats.DiskPercent()),
			loadCell(status.Control.Stats),
		}}))
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, util.Title(fmt.Sprintf("NODES (%d)", len(status.Nodes))))
	if len(status.Nodes) == 0 {
		fmt.Fprintf(w, "  none registered\n")
	} else {
		rows := make([][]string, 0, len(status.Nodes))
		for _, n := range status.Nodes {
			rows = append(rows, []string{
				n.Name, dashIfEmpty(n.Address), strconv.Itoa(n.Apps),
				statCell(n.Stats.MemoryUsedMB, n.Stats.MemoryTotalMB, n.Stats.MemoryPercent()),
				statCell(n.Stats.DiskUsedMB, n.Stats.DiskTotalMB, n.Stats.DiskPercent()),
				loadCell(n.Stats),
				seenLabel(n.LastSeen, n.Stale, status.Snapshot),
			})
		}
		fmt.Fprintln(w, util.Render([]string{"NAME", "ADDRESS", "APPS", "RAM", "DISK", "LOAD / CPUS", "SEEN"}, rows))
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, util.Title(fmt.Sprintf("PROXIES (%d)", len(status.Proxies))))
	if len(status.Proxies) == 0 {
		fmt.Fprintf(w, "  none registered\n")
	} else {
		rows := make([][]string, 0, len(status.Proxies))
		for _, p := range status.Proxies {
			rows = append(rows, []string{
				p.Name, dashIfEmpty(shortVersion(p.Version)), strconv.Itoa(p.Routes),
				statCell(p.Stats.MemoryUsedMB, p.Stats.MemoryTotalMB, p.Stats.MemoryPercent()),
				statCell(p.Stats.DiskUsedMB, p.Stats.DiskTotalMB, p.Stats.DiskPercent()),
				loadCell(p.Stats),
				seenLabel(p.LastSeen, p.Stale, status.Snapshot),
			})
		}
		fmt.Fprintln(w, util.Render([]string{"NAME", "VERSION", "ROUTES", "RAM", "DISK", "LOAD / CPUS", "SEEN"}, rows))
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, util.Title("APPS"))
	fmt.Fprintf(w, "  %d total, %d powered off, %d snapshots, %s on disk\n",
		status.Apps.Total, status.Apps.PoweredOff, status.Apps.Snapshots, humanMB(status.Apps.DiskUsedMB))
	if status.Apps.Unplaced > 0 {
		fmt.Fprintf(w, "  %d on a node that is not registered -- they are not routable\n", status.Apps.Unplaced)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, util.Title("PEOPLE"))
	fmt.Fprintf(w, "  %d total, %d admins", status.People.Total, status.People.Admins)
	if status.People.Pending > 0 {
		fmt.Fprintf(w, ", %d awaiting approval", status.People.Pending)
	}
	fmt.Fprintln(w)

	// Say plainly what this is and is not: every number above is what the daemon
	// last wrote down, so a stopped daemon shows its final word rather than
	// nothing, and nothing here claims an app is currently serving.
	if stale := staleMembers(status); stale > 0 {
		fmt.Fprintf(w, "\n%d member(s) have not reported recently. Check: systemctl status hostit-control\n", stale)
	}
}

func staleMembers(status *control.Status) int {
	n := 0
	for _, m := range append(append([]*control.MemberStatus{}, status.Nodes...), status.Proxies...) {
		if m.Stale {
			n++
		}
	}
	return n
}

// seenLabel renders a heartbeat as an age, which is what an operator reads it
// as; a stale one says so rather than making them do the subtraction.
func seenLabel(lastSeen time.Time, stale bool, now time.Time) string {
	if lastSeen.IsZero() {
		return "never reported"
	}
	age := now.Sub(lastSeen).Round(time.Second)
	if stale {
		return fmt.Sprintf("LAST SEEN %s ago", age)
	}
	return fmt.Sprintf("seen %s ago", age)
}

// shortVersion keeps the leading version out of the build metadata, so a row
// stays one line.
func shortVersion(version string) string {
	for i, r := range version {
		if r == ' ' {
			return version[:i]
		}
	}
	return version
}

func humanMB(mb int) string {
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GB", float64(mb)/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
