package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v2"
	"heckel.io/hostit/assistant"
	"heckel.io/hostit/control/config"
)

// cmdInternal groups host-side debug/plumbing commands that app owners never run.
// It is hidden from the command list; the other internal commands (shell, enter,
// agent, mcp) stay top-level because external wrappers, sudoers, the systemd unit
// and claude's mcp-config invoke them by name.
var (
	cmdInternal = &cli.Command{
		Name:        "internal",
		Usage:       "internal debug commands (not for app owners)",
		Hidden:      true,
		Subcommands: []*cli.Command{cmdInternalAssistant},
	}
)

var (
	cmdInternalAssistant = &cli.Command{
		Name:      "assistant",
		Usage:     "run one assistant turn via the sandboxed Claude Max backend (debug)",
		ArgsUsage: "<app> [prompt]",
		Flags: []cli.Flag{
			// The subscription token is a CONTROL setting, so this reads control's
			// config -- pointing it at the node's would parse (yaml is lenient) and
			// then report the token missing from a file it never belonged in.
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: config.DefaultControlConfigFile, Usage: "control config file"},
			&cli.BoolFlag{Name: "shell", Usage: "drop into a shell in the sandbox instead of running claude (debugging)"},
			&cli.BoolFlag{Name: "raw", Usage: "print raw event fields instead of a pretty summary"},
		},
		Action: execInternalAssistant,
	}
)

func execInternalAssistant(c *cli.Context) error {
	conf, err := config.LoadConfig(c.String("config"))
	if err != nil {
		return err
	}
	if !conf.ClaudeBackendEnabled() {
		return fmt.Errorf("no claude-code-oauth-token in %s: the Claude Max backend is not configured", c.String("config"))
	}
	appName := c.Args().Get(0)
	if appName == "" {
		return fmt.Errorf("usage: hostit internal assistant <app> [prompt]")
	}
	prompt := strings.Join(c.Args().Slice()[1:], " ")
	if prompt == "" && !c.Bool("shell") {
		return fmt.Errorf("usage: hostit internal assistant <app> <prompt>  (or --shell for a debug shell)")
	}
	sandbox, err := assistant.NewSandbox(conf)
	if err != nil {
		return err
	}
	if c.Bool("shell") {
		return sandbox.Shell(appName)
	}
	fmt.Fprintf(os.Stderr, "==> assistant turn for app=%s\n", appName)
	return sandbox.RunTurn(context.Background(), appName, prompt, "", nil, printAssistantEvent(c.Bool("raw")))
}

// printAssistantEvent renders one sandbox event as a compact human summary (or
// the raw fields with --raw): the model's text, each tool it calls, each result,
// and the final result with token usage.
func printAssistantEvent(raw bool) func(assistant.StreamEvent) {
	return func(ev assistant.StreamEvent) {
		if raw {
			fmt.Printf("%+v\n", ev)
			return
		}
		switch ev.Type {
		case "init":
			fmt.Printf("[init] model=%s tools=%s\n", ev.Model, strings.Join(ev.Tools, ","))
		case "text":
			if s := strings.TrimSpace(ev.Text); s != "" {
				fmt.Printf("\n%s\n", s)
			}
		case "tool_use":
			fmt.Printf("  -> %s %s\n", ev.Tool, ev.Input)
		case "tool_result":
			marker := "   "
			if ev.IsError {
				marker = " ! "
			}
			fmt.Printf("%s%s\n", marker, firstLine(ev.Output))
		case "result":
			fmt.Printf("\n=== result ===\n%s\n", ev.Result)
			if ev.Usage != nil {
				fmt.Printf("usage: in=%d out=%d cache_write=%d cache_read=%d\n", ev.Usage.InputTokens, ev.Usage.OutputTokens, ev.Usage.CacheWriteTokens, ev.Usage.CacheReadTokens)
			}
		case "error":
			fmt.Printf("\n=== error ===\n%s\n", ev.ErrorMsg)
		}
	}
}

// firstLine keeps a tool result summary to one line so the stream stays readable.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	if len(s) > 200 {
		return s[:200] + " ..."
	}
	return s
}
