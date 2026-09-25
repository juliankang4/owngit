package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"owngit/internal/server"
	"owngit/internal/state"
	"owngit/internal/tailscale"
	"owngit/internal/webui"
)

// "owngit tailscale" shares OwnGit on the tailnet over HTTPS with the host's
// Tailscale Serve, with the same rules as the Tailscale section of Settings
// (server.Tailscale). Like "owngit network set" it needs local access to the
// state directory. A server that is running does not pick up a change made
// here: the command has no channel into that process, so it says when a
// restart is needed. Turning sharing on or off in Settings changes the
// running server at once.

// findTailscale finds the tailscale command; tests replace it so that no
// test can reach a real tailscale.
var findTailscale = tailscale.Find

func tailscaleCommand(arguments []string) error {
	if len(arguments) == 0 {
		printTailscaleUsage(os.Stderr)
		return cliProblem("invalid_arguments", "tailscale requires status, on, or off.")
	}
	if isHelpArgument(arguments[0]) {
		printTailscaleUsage(os.Stdout)
		return nil
	}
	switch arguments[0] {
	case "status":
		return tailscaleStatus(arguments[1:])
	case "on":
		return tailscaleOn(arguments[1:])
	case "off":
		return tailscaleOff(arguments[1:])
	default:
		return cliProblem("invalid_arguments", "Unknown tailscale command: "+arguments[0])
	}
}

func printTailscaleUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit tailscale <status|on|off> [options]")
	fmt.Fprintln(writer, "  tailscale status [--json]                  whether OwnGit is shared on the tailnet over HTTPS, and what is missing")
	fmt.Fprintln(writer, "  tailscale on [--home-network[=false]]      share OwnGit at https://NAME.TAILNET.ts.net/ with Tailscale Serve")
	fmt.Fprintln(writer, "  tailscale off                              remove the Tailscale address OwnGit made and the settings it changed")
	fmt.Fprintln(writer, "Tailscale must be installed and signed in on this computer, with MagicDNS and HTTPS certificates on in the tailnet.")
	fmt.Fprintln(writer, "on and off change the saved settings; a running OwnGit uses them after a restart. The Settings page applies them at once.")
}

// tailscaleFlags are the options every tailscale command takes.
type tailscaleFlags struct {
	flags    *flag.FlagSet
	stateDir *string
	command  *string
	asJSON   *bool
}

func newTailscaleFlags(name string) tailscaleFlags {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return tailscaleFlags{
		flags:    flags,
		stateDir: flags.String("state-dir", defaultStateDir(), "host-local state directory"),
		command:  flags.String("tailscale", "", "tailscale command `path`; found automatically when empty"),
		asJSON:   flags.Bool("json", false, "print JSON"),
	}
}

// sharing opens the state and returns the Tailscale sharing of it, as seen
// from outside a running server.
func (options tailscaleFlags) sharing(ctx context.Context) (*server.Tailscale, *state.Store, error) {
	store, err := openLiveState(ctx, *options.stateDir)
	if err != nil {
		return nil, nil, err
	}
	return &server.Tailscale{
		Store:   store,
		Find:    func() (tailscale.Command, error) { return findTailscale(*options.command) },
		Observe: store.ObserveRunningNetwork,
	}, store, nil
}

func tailscaleStatus(arguments []string) error {
	options := newTailscaleFlags("tailscale status")
	if err := parseFlags(options.flags, arguments); err != nil {
		return err
	}
	if options.flags.NArg() != 0 {
		return errors.New("tailscale status accepts no positional arguments")
	}
	if err := state.RequireExisting(*options.stateDir); err != nil {
		return err
	}
	ctx := context.Background()
	sharing, store, err := options.sharing(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	report, err := sharing.Report(ctx)
	if err != nil {
		return err
	}
	if *options.asJSON {
		return printJSON(report)
	}
	printTailscaleReport(os.Stdout, report)
	return nil
}

func tailscaleOn(arguments []string) error {
	options := newTailscaleFlags("tailscale on")
	homeNetwork := options.flags.Bool("home-network", false, "also let devices on the home network connect over plain HTTP; --home-network=false keeps OwnGit on this computer only. Without it the listen address stays as it is when Tailscale can reach it")
	if err := parseFlags(options.flags, arguments); err != nil {
		return err
	}
	if options.flags.NArg() != 0 {
		return errors.New("tailscale on accepts no positional arguments")
	}
	if err := state.RequireExisting(*options.stateDir); err != nil {
		return err
	}
	var choice *bool
	options.flags.Visit(func(entry *flag.Flag) {
		if entry.Name == "home-network" {
			choice = homeNetwork
		}
	})
	ctx := context.Background()
	sharing, store, err := options.sharing(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	change, err := sharing.On(ctx, choice)
	if err != nil {
		return tailscaleFailure(err)
	}
	report, err := sharing.Report(ctx)
	if err != nil {
		return err
	}
	if *options.asJSON {
		return printJSON(report)
	}
	switch {
	case change.Endpoint == "created":
		fmt.Printf("Tailscale now answers HTTPS for %s and passes it to OwnGit at %s.\n", change.Record.Name, change.Record.Target)
	case change.Record.Created:
		fmt.Printf("Tailscale already answered HTTPS for %s with the address OwnGit made, passing it to OwnGit at %s.\n", change.Record.Name, change.Record.Target)
	default:
		fmt.Printf("Tailscale already answered HTTPS for %s with OwnGit at %s; OwnGit left that setting as it was.\n", change.Record.Name, change.Record.Target)
	}
	fmt.Printf("Saved: base URL %s, allowed name %s, trusted proxy 127.0.0.1.\n", change.Record.BaseURL, change.Record.Name)
	if change.ListenChanged {
		fmt.Printf("OwnGit listens on %s from the next start.\n", change.Listen)
	}
	printTailscaleReport(os.Stdout, report)
	observed, err := store.ObserveRunningNetwork(ctx)
	if err != nil {
		return err
	}
	printRunningServerNote(observed.Server)
	return nil
}

// printRunningServerNote says, when a server may be running, that this
// command changed only the saved settings, because it cannot reach that
// server, and that the Settings page applies the change at once.
func printRunningServerNote(server string) {
	if server == state.ServerRunning || server == state.ServerUnknown {
		fmt.Println("This command cannot change a running OwnGit, so the change applies after a restart. Turning sharing on or off in Settings applies at once.")
	}
}

func tailscaleOff(arguments []string) error {
	options := newTailscaleFlags("tailscale off")
	if err := parseFlags(options.flags, arguments); err != nil {
		return err
	}
	if options.flags.NArg() != 0 {
		return errors.New("tailscale off accepts no positional arguments")
	}
	if err := state.RequireExisting(*options.stateDir); err != nil {
		return err
	}
	ctx := context.Background()
	sharing, store, err := options.sharing(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	change, err := sharing.Off(ctx)
	if err != nil {
		return tailscaleFailure(err)
	}
	observed, err := store.ObserveRunningNetwork(ctx)
	if err != nil {
		return err
	}
	if *options.asJSON {
		return printJSON(struct {
			Endpoint string `json:"endpoint"`
			Name     string `json:"name"`
			Server   string `json:"server"`
		}{change.Endpoint, change.Record.Name, observed.Server})
	}
	switch change.Endpoint {
	case "removed":
		fmt.Printf("Tailscale no longer answers HTTPS for %s.\n", change.Record.Name)
	case "gone":
		fmt.Printf("Tailscale had already stopped answering HTTPS for %s.\n", change.Record.Name)
	default:
		fmt.Printf("OwnGit did not create the Tailscale address for %s, so it left it in place. Remove it with \"tailscale serve --https=%d off\" if you no longer need it.\n", change.Record.Name, change.Record.HTTPSPort)
	}
	fmt.Println("The base URL, allowed name and trusted proxy that sharing added were removed where they were still saved. The listen address is unchanged.")
	if observed.Server == state.ServerRunning || observed.Server == state.ServerUnknown {
		fmt.Println("Restart OwnGit so the running server stops accepting the Tailscale address.")
	}
	printRunningServerNote(observed.Server)
	return nil
}

// tailscaleFailure explains a refusal in the words the Settings page uses.
func tailscaleFailure(err error) error {
	var refusal *server.TailscaleError
	if !errors.As(err, &refusal) {
		return err
	}
	return errors.New(tailscaleProblemText(refusal.Problem, refusal.Detail, refusal.Found, refusal.MacApp))
}

// tailscaleProblemText is the English message for a problem.
func tailscaleProblemText(problem, detail string, found []string, macApp bool) string {
	text := webui.Text(webui.LangEN, webui.TailscaleProblemCode(problem))
	if len(found) > 0 {
		detail = strings.Join(found, "; ")
	}
	if detail != "" && strings.HasSuffix(text, ":") {
		text += " " + detail
	}
	if problem == server.TailscaleProblemReadBack && macApp {
		text += " " + webui.Text(webui.LangEN, webui.MsgTSReadBackMacApp)
	}
	return text
}

func printTailscaleReport(writer io.Writer, report server.TailscaleReport) {
	switch {
	case report.On && report.Ready:
		fmt.Fprintln(writer, "Sharing on the tailnet over HTTPS: on and ready.")
	case report.On:
		fmt.Fprintln(writer, "Sharing on the tailnet over HTTPS: on, but not ready yet.")
	default:
		fmt.Fprintln(writer, "Sharing on the tailnet over HTTPS: off.")
	}
	if report.On {
		fmt.Fprintf(writer, "  Address:   %s (encrypted by Tailscale on this computer)\n", report.URL)
		fmt.Fprintf(writer, "  Clone URL: %sgit/REPOSITORY.git\n", report.URL)
	}
	if report.Installed {
		fmt.Fprintf(writer, "  Tailscale: %s\n", report.Command)
	}
	if report.Problem != "" {
		fmt.Fprintf(writer, "  %s\n", tailscaleProblemText(report.Problem, report.ProblemDetail, nil, report.MacApp))
	} else if !report.On && report.Name != "" {
		fmt.Fprintf(writer, "  Ready to share as https://%s/.\n", report.Name)
	}
	if report.Endpoint == server.TailscaleEndpointTaken || report.Endpoint == server.TailscaleEndpointChanged {
		fmt.Fprintf(writer, "  HTTPS port %d of Tailscale also has: %s\n", server.TailscaleHTTPSPort, strings.Join(report.Found, "; "))
	}
	for _, wait := range report.Waiting {
		if wait != server.TailscaleWaitTailscale {
			fmt.Fprintf(writer, "  %s\n", webui.Text(webui.LangEN, webui.TailscaleWaitCode(wait)))
		}
	}
	if report.MacApp {
		fmt.Fprintf(writer, "  %s\n", webui.Text(webui.LangEN, webui.MsgTSMacApp))
	}
	if !report.On && report.Problem == "" {
		fmt.Fprintln(writer, "Turn it on with \"owngit tailscale on\", or in Settings.")
	}
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
