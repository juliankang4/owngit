package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"owngit/internal/apiclient"
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
	fmt.Fprintln(writer, "  tailscale on [--home-network[=false]] [--https-port PORT [--replace-endpoint DIGEST]]")
	fmt.Fprintln(writer, "                                             share OwnGit at https://NAME.TAILNET.ts.net/ with Tailscale Serve;")
	fmt.Fprintln(writer, "                                             if HTTPS port 443 serves something else, OwnGit uses 8443 or 10000;")
	fmt.Fprintln(writer, "                                             another --https-port moves sharing that is on; --replace-endpoint")
	fmt.Fprintln(writer, "                                             replaces what that port has, as status reviewed it (its digest)")
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
	if err := state.RequireExisting(*options.stateDir); err != nil {
		return nil, nil, err
	}
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

// failure returns err so that it prints as a JSON error object under
// --json: a refusal gets its problem as the code and the words of the
// Settings page, and a missing state says how to create it. Other errors
// get code.
func (options tailscaleFlags) failure(code string, err error) error {
	var refusal *server.TailscaleError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &refusal):
		// The explanation replaces the error, so it keeps saying that the
		// settings were not saved when Tailscale may be ahead of them.
		explained := tailscaleFailure(refusal)
		if errors.Is(err, server.ErrTailscaleAhead) {
			explained = fmt.Errorf("%w: %w", server.ErrTailscaleAhead, explained)
		}
		if errors.Is(err, server.ErrTailscaleMoveStopped) {
			explained = fmt.Errorf("%w: %w", server.ErrTailscaleMoveStopped, explained)
		}
		code, err = refusal.Problem, explained
	case errors.Is(err, state.ErrNotExist):
		code, err = "state_missing", missingStateError(*options.stateDir)
	}
	return jsonFailure(*options.asJSON, code, err)
}

// missingStateError explains a state directory without OwnGit's state, as
// before the first start.
func missingStateError(stateDir string) error {
	return fmt.Errorf("OwnGit state does not exist in %s. Start OwnGit once with this state directory, or pass the one it uses with --state-dir", stateDir)
}

// jsonFailure makes err print as a JSON error object with code when the
// command was asked for JSON. An error that already has a code keeps it.
func jsonFailure(asJSON bool, code string, err error) error {
	var coded interface{ ErrorCode() string }
	if err == nil || !asJSON || errors.As(err, &coded) {
		return err
	}
	return &apiclient.Error{Code: code, Message: err.Error(), Cause: err}
}

// parseFlagsJSON is parseFlags for a command with a --json option: an
// option it cannot read is a JSON error object when --json was given,
// wherever it appears, although parsing stopped before it.
func parseFlagsJSON(flags *flag.FlagSet, arguments []string) error {
	err := parseFlags(flags, arguments)
	if err == nil || errors.Is(err, errUsageShown) {
		return err
	}
	return jsonFailure(jsonRequested(arguments), "invalid_arguments", err)
}

// jsonRequested reports whether arguments give the --json option as true,
// before any "--".
func jsonRequested(arguments []string) bool {
	requested := false
	for _, argument := range arguments {
		if argument == "--" {
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(argument, "-"), "-"), "=")
		if !strings.HasPrefix(argument, "-") || name != "json" {
			continue
		}
		requested = true
		if hasValue {
			requested, _ = strconv.ParseBool(value)
		}
	}
	return requested
}

func tailscaleStatus(arguments []string) error {
	options := newTailscaleFlags("tailscale status")
	if err := parseFlagsJSON(options.flags, arguments); err != nil {
		return err
	}
	if options.flags.NArg() != 0 {
		return options.failure("invalid_arguments", errors.New("tailscale status accepts no positional arguments"))
	}
	ctx := context.Background()
	sharing, store, err := options.sharing(ctx)
	missing := errors.Is(err, state.ErrNotExist)
	switch {
	case missing:
		// Before the first start nothing is saved: the report shows the
		// defaults and creates nothing.
		sharing = &server.Tailscale{Find: func() (tailscale.Command, error) { return findTailscale(*options.command) }}
	case err != nil:
		return options.failure("state_unavailable", err)
	default:
		defer store.Close()
	}
	report, err := sharing.Report(ctx)
	if err != nil {
		return options.failure("state_unavailable", err)
	}
	if *options.asJSON {
		return printJSON(struct {
			server.TailscaleReport
			// StateMissing says that the state directory holds no OwnGit
			// state yet, so the report shows the defaults.
			StateMissing bool `json:"state_missing,omitempty"`
		}{report, missing})
	}
	if missing {
		fmt.Printf("No OwnGit state exists in %s yet, so nothing is saved and the defaults apply.\n", *options.stateDir)
	}
	printTailscaleReport(os.Stdout, report)
	return nil
}

func tailscaleOn(arguments []string) error {
	options := newTailscaleFlags("tailscale on")
	homeNetwork := options.flags.Bool("home-network", false, "also let devices on the home network connect over plain HTTP; --home-network=false keeps OwnGit on this computer only. Without it the listen address stays as it is when Tailscale can reach it")
	httpsPort := options.flags.Int("https-port", 0, "the HTTPS `port` Tailscale answers on; without it OwnGit uses 443, or 8443 or 10000 when something else is on 443, and keeps the port sharing uses now. Another port moves sharing that is on")
	replace := options.flags.String("replace-endpoint", "", "replace what another service has on --https-port with OwnGit's address: the `digest` \"owngit tailscale status\" or a refusal showed for that port. Nothing is replaced when anything Tailscale serves changed since")
	if err := parseFlagsJSON(options.flags, arguments); err != nil {
		return err
	}
	if options.flags.NArg() != 0 {
		return options.failure("invalid_arguments", errors.New("tailscale on accepts no positional arguments"))
	}
	var choice *bool
	portSet := false
	options.flags.Visit(func(entry *flag.Flag) {
		switch entry.Name {
		case "home-network":
			choice = homeNetwork
		case "https-port":
			portSet = true
		}
	})
	if portSet && (*httpsPort < 1 || *httpsPort > 65535) {
		return options.failure("invalid_arguments", errors.New("--https-port must be a port from 1 to 65535"))
	}
	if *replace != "" && !portSet {
		return options.failure("invalid_arguments", errors.New("--replace-endpoint needs the --https-port it replaces"))
	}
	ctx := context.Background()
	sharing, store, err := options.sharing(ctx)
	if err != nil {
		return options.failure("state_unavailable", err)
	}
	defer store.Close()
	// The certificate log notice comes before Tailscale is asked to serve
	// the address, as it stands next to the switch in Settings. Command
	// output other than terminal setup is English.
	var certificateLog string
	sharing.BeforeServe = func(name string) {
		certificateLog = fmt.Sprintf(webui.Text(webui.LangEN, webui.MsgTSCertLog), name)
		if !*options.asJSON {
			fmt.Println(certificateLog)
		}
	}
	var change server.TailscaleChange
	if *replace != "" {
		change, err = sharing.Replace(ctx, choice, *httpsPort, *replace)
	} else {
		change, err = sharing.On(ctx, choice, *httpsPort)
	}
	if err != nil {
		return options.failure("failed", err)
	}
	report, err := sharing.Report(ctx)
	if err != nil {
		return options.failure("state_unavailable", err)
	}
	if *options.asJSON {
		return printJSON(struct {
			server.TailscaleReport
			// CertificateLog is the notice shown before the address was
			// served, or empty when turning on served nothing new.
			CertificateLog string `json:"certificate_log,omitempty"`
		}{report, certificateLog})
	}
	address := change.Record.Name
	if port := change.Record.HTTPSPort; port != 443 {
		address = fmt.Sprintf("%s on port %d", address, port)
	}
	switch {
	case change.Endpoint == "created":
		fmt.Printf("Tailscale now answers HTTPS for %s and passes it to OwnGit at %s.\n", address, change.Record.Target)
	case change.Record.Created:
		fmt.Printf("Tailscale already answered HTTPS for %s with the address OwnGit made, passing it to OwnGit at %s.\n", address, change.Record.Target)
	default:
		fmt.Printf("Tailscale already answered HTTPS for %s with OwnGit at %s; OwnGit left that setting as it was.\n", address, change.Record.Target)
	}
	if change.MovedFrom != "" {
		fmt.Printf("Sharing moved from %s. Clones that use that address need the new one, for example with git remote set-url.\n", change.MovedFrom)
	}
	if len(change.PassedPorts) > 0 {
		fmt.Println(fmt.Sprintf(webui.Text(webui.LangEN, webui.TailscalePortNote(len(change.PassedPorts))), server.PortList(change.PassedPorts), strconv.Itoa(change.Record.HTTPSPort)))
	}
	if change.Endpoint == "created" {
		fmt.Println(webui.Text(webui.LangEN, webui.MsgTSFirstVisit))
	}
	fmt.Printf("Saved: base URL %s, allowed name %s, trusted proxy 127.0.0.1.\n", change.Record.BaseURL, change.Record.Name)
	if change.ListenChanged {
		fmt.Printf("OwnGit listens on %s from the next start.\n", change.Listen)
	}
	if change.ListenOption != "" && choice != nil {
		fmt.Printf("The running OwnGit was started with --listen %s, which decides where it listens, so --home-network changed nothing.\n", change.ListenOption)
	}
	printTailscaleReport(os.Stdout, report)
	observed, err := store.ObserveRunningNetwork(ctx)
	if err != nil {
		return options.failure("state_unavailable", err)
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
	if err := parseFlagsJSON(options.flags, arguments); err != nil {
		return err
	}
	if options.flags.NArg() != 0 {
		return options.failure("invalid_arguments", errors.New("tailscale off accepts no positional arguments"))
	}
	ctx := context.Background()
	sharing, store, err := options.sharing(ctx)
	if err != nil {
		return options.failure("state_unavailable", err)
	}
	defer store.Close()
	change, err := sharing.Off(ctx)
	if err != nil {
		return options.failure("failed", err)
	}
	observed, err := store.ObserveRunningNetwork(ctx)
	if err != nil {
		return options.failure("state_unavailable", err)
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
	case "stale":
		fmt.Printf("This computer is no longer named %s in the tailnet, so Tailscale no longer answers for that name. %s https://%s:%d/\n",
			change.Record.Name, webui.Text(webui.LangEN, webui.MsgTSStale), change.Record.Name, change.Record.HTTPSPort)
	default:
		fmt.Printf("OwnGit did not create the Tailscale address for %s, so it left it in place. Remove it with \"tailscale serve --https=%d off\" if you no longer need it.\n", change.Record.Name, change.Record.HTTPSPort)
	}
	fmt.Println("The base URL, allowed name and trusted proxy that sharing added were removed where they were still saved. The listen address is unchanged.")
	printRunningServerNote(observed.Server)
	return nil
}

// tailscaleFailure explains a refusal in the words the Settings page uses.
func tailscaleFailure(refusal *server.TailscaleError) error {
	text := tailscaleProblemText(refusal.Problem, refusal.Detail, refusal.Found, refusal.MacApp)
	switch refusal.Problem {
	case server.TailscaleProblemChanged:
		text += " " + tailscaleFixText(refusal.Found, true, refusal.Target, refusal.Port)
	case server.TailscaleProblemUnrecorded:
		text += " " + tailscaleFixText(refusal.Found, false, "", refusal.Port)
	}
	if refusal.Occupied != nil {
		text += " " + replaceText(*refusal.Occupied)
	}
	return errors.New(text)
}

// replaceText says how to replace what another service has on a port, or
// why OwnGit does not.
func replaceText(occupied server.TailscaleOccupied) string {
	if !occupied.Replaceable {
		return webui.Text(webui.LangEN, webui.MsgTSReplaceNotAllowed)
	}
	return fmt.Sprintf("If you no longer need what is on port %d, replace it with \"owngit tailscale on --https-port %d --replace-endpoint %s\"; it then stops answering there.", occupied.Port, occupied.Port, occupied.Digest)
}

// tailscaleFixText is the English step that clears what is on HTTPS port
// (server.TailscaleFix).
func tailscaleFixText(found []tailscale.Use, changed bool, target string, port int) string {
	code, value := server.TailscaleFix(found, changed, target)
	return fmt.Sprintf(webui.Text(webui.LangEN, code), strconv.Itoa(port), value)
}

// tailscaleProblemText is the English message for a problem.
func tailscaleProblemText(problem, detail string, found []tailscale.Use, macApp bool) string {
	text := webui.Text(webui.LangEN, webui.TailscaleProblemCode(problem))
	if len(found) > 0 {
		detail = server.TailscaleUsesText(found)
	}
	switch {
	case detail == "":
	case strings.HasSuffix(text, ":"):
		text += " " + detail
	default:
		text += " Tailscale said: " + detail
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
		// A --base-url option keeps the running server giving out its own
		// address, so the HTTPS clone address is not claimed then.
		if report.BaseURLOption != "" {
			fmt.Fprintf(writer, "  %s\n", fmt.Sprintf(webui.Text(webui.LangEN, webui.MsgTSBaseURLOption), report.BaseURLOption))
		} else {
			fmt.Fprintf(writer, "  Clone URL: %sgit/REPOSITORY.git\n", report.URL)
		}
	}
	if report.Installed {
		fmt.Fprintf(writer, "  Tailscale: %s\n", report.Command)
	}
	// The verdict is the report's, as on the Settings page.
	if report.Problem != "" {
		fmt.Fprintf(writer, "  %s\n", tailscaleProblemText(report.Problem, report.ProblemDetail, nil, report.MacApp))
	} else if !report.On && report.CanTurnOn {
		fmt.Fprintf(writer, "  Ready to share as %s/.\n", server.TailscaleOrigin(report.Name, report.TurnOnPort))
		if len(report.TurnOnPassed) > 0 {
			fmt.Fprintf(writer, "  %s\n", fmt.Sprintf(webui.Text(webui.LangEN, webui.TailscalePortNote(len(report.TurnOnPassed))), server.PortList(report.TurnOnPassed), strconv.Itoa(report.TurnOnPort)))
		}
	}
	switch {
	case report.On && report.Endpoint == server.TailscaleEndpointChanged:
		fmt.Fprintf(writer, "  %s %s\n", webui.Text(webui.LangEN, webui.MsgTSChanged), server.TailscaleUsesText(report.Found))
		fmt.Fprintf(writer, "  %s\n", tailscaleFixText(report.Found, true, report.Sharing.Target, report.Sharing.HTTPSPort))
	case report.PortsTaken:
		fmt.Fprintf(writer, "  %s %s\n  %s\n", webui.Text(webui.LangEN, webui.MsgTSTaken), server.TailscaleUsesText(report.Found), webui.Text(webui.LangEN, webui.MsgTSTakenSteps))
		for _, occupied := range report.Occupied {
			fmt.Fprintf(writer, "  Port %d: %s %s\n", occupied.Port, server.TailscaleUsesText(occupied.Found), replaceText(occupied))
		}
	case !report.On && report.Endpoint == server.TailscaleEndpointUnrecorded:
		fmt.Fprintf(writer, "  %s %s\n  %s\n", webui.Text(webui.LangEN, webui.MsgTSUnrecorded), server.TailscaleUsesText(report.Found), tailscaleFixText(report.Found, false, "", report.TurnOnPort))
	}
	for _, wait := range report.Waiting {
		if wait != server.TailscaleWaitTailscale {
			fmt.Fprintf(writer, "  %s\n", webui.Text(webui.LangEN, webui.TailscaleWaitCode(wait)))
		}
	}
	if len(report.Stale) > 0 {
		fmt.Fprintf(writer, "  %s %s\n", webui.Text(webui.LangEN, webui.MsgTSStale), server.TailscaleUsesText(report.Stale))
	}
	if report.MacApp {
		fmt.Fprintf(writer, "  %s\n", webui.Text(webui.LangEN, webui.MsgTSMacApp))
	}
	switch {
	case report.CanTurnOn && report.On:
		fmt.Fprintln(writer, "Turn it on again with \"owngit tailscale on\", or in Settings.")
	case report.CanTurnOn:
		fmt.Fprintln(writer, "Turn it on with \"owngit tailscale on\", or in Settings.")
	}
}

// printJSON prints value as indented JSON through writeJSON, so direction
// controls are escaped like in every JSON result. It is never HTML, so &, <
// and > in a command or URL stay readable.
func printJSON(value any) error {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return &apiclient.Error{Code: "output_failed", Message: "The JSON result could not be encoded.", Cause: err}
	}
	return writeJSON(output.Bytes())
}
