package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"owngit/internal/auth"
	"owngit/internal/bootstrap"
	"owngit/internal/gitexec"
	"owngit/internal/githttp"
	"owngit/internal/pullrequest"
	"owngit/internal/recovery"
	"owngit/internal/repository"
	"owngit/internal/server"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if !writeStructuredCommandError(os.Stdout, err) {
			log.Printf("error: %v", err)
		}
		os.Exit(1)
	}
}

func run(arguments []string) error {
	// Global help must be handled before command dispatch: the flag package
	// treats -h/--help as a request for help, and without this check those
	// flags fall into "serve" and make it exit with a flag error.
	if len(arguments) > 0 && (arguments[0] == "help" || arguments[0] == "-h" || arguments[0] == "--help") {
		printUsage(os.Stdout)
		return nil
	}
	command := "serve"
	if len(arguments) != 0 && !strings.HasPrefix(arguments[0], "-") {
		command, arguments = arguments[0], arguments[1:]
	}
	switch command {
	case "serve":
		return serve(arguments)
	case "setup-link":
		return setupLink(arguments)
	case "reset-admin":
		return resetAdmin(arguments)
	case "approve-host":
		return approveHost(arguments)
	case "backup":
		return backupState(arguments)
	case "restore":
		return restoreState(arguments)
	case "pr":
		return prCommand(arguments)
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", command)
	}
}

func serve(arguments []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	listenAddress := flags.String("listen", "127.0.0.1:7654", "HTTP listen address")
	baseURL := flags.String("base-url", "", "owner-facing HTTP origin")
	gitPath := flags.String("git", "", "Git executable path")
	noOpen := flags.Bool("no-open", false, "do not open the private setup file")
	var allowedHosts stringList
	flags.Var(&allowedHosts, "allowed-host", "additional accepted Host name (repeatable)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("serve does not accept positional arguments")
	}

	ctx := context.Background()
	store, err := state.Open(ctx, *stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	unlock, err := state.AcquireOfflineLock(store.Dir())
	if err != nil {
		return err
	}
	defer unlock()
	runner, err := gitexec.New(*gitPath, filepath.Join(store.Dir(), "runtime"))
	if err != nil {
		return err
	}
	versionResult, err := runner.Run(ctx, "", nil, "--version")
	if err != nil {
		return err
	}
	backendPath, err := githttp.DiscoverBackend(ctx, runner)
	if err != nil {
		return err
	}
	renderer, err := webui.New()
	if err != nil {
		return err
	}
	settings, err := store.Settings(ctx)
	if err != nil {
		return err
	}
	repositories := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: settings.RepositoryRoot}
	pullRequests := &pullrequest.Service{Store: store, Repositories: repositories}
	if settings.Initialized {
		if err := repositories.PrepareExisting(ctx); err != nil {
			return err
		}
		if err := pullRequests.ReconcileAll(ctx); err != nil {
			return err
		}
	}
	gitHandler, err := githttp.New(runner, repositories, backendPath, 4)
	if err != nil {
		return err
	}
	authentication := &auth.Manager{Store: store, SessionLife: 12 * time.Hour, AdminSessionLife: 15 * time.Minute}
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", *listenAddress, err)
	}
	defer listener.Close()

	policy := server.NewHostPolicy(allowedHosts...)
	trusted, err := store.TrustedHosts(ctx)
	if err != nil {
		return err
	}
	for _, host := range trusted {
		if err := policy.Add(host); err != nil {
			return fmt.Errorf("invalid stored trusted host: %w", err)
		}
	}
	if host, _, err := net.SplitHostPort(*listenAddress); err == nil && host != "" && host != "0.0.0.0" && host != "::" {
		if err := policy.Add(host); err != nil {
			return err
		}
	}
	originAddress := *listenAddress
	if *baseURL == "" {
		originAddress = listener.Addr().String()
	}
	origin, err := ownerOrigin(*baseURL, originAddress)
	if err != nil {
		return err
	}
	parsedOrigin, _ := url.Parse(origin)
	if err := policy.Add(parsedOrigin.Host); err != nil {
		return fmt.Errorf("trust setup origin: %w", err)
	}

	home, _ := os.UserHomeDir()
	application := &server.App{
		Store: store, Auth: authentication, Repositories: repositories, PullRequests: pullRequests, GitHTTP: gitHandler,
		Renderer: renderer, Hosts: policy, SuggestedRepositoryRoot: filepath.Join(home, "OwnGit-Repositories"),
		GitVersion: strings.TrimSpace(string(versionResult.Stdout)), HTTPBackendFound: true,
	}
	gitHandler.Authorize = application.AuthorizeGit

	if !settings.Initialized {
		path, err := (&bootstrap.Issuer{Store: store, BaseURL: origin}).Issue(ctx)
		if err != nil {
			return err
		}
		log.Printf("owner setup file: %s", path)
		if !*noOpen {
			if err := bootstrap.Open(path); err != nil {
				log.Printf("could not open setup file automatically: %v", err)
			}
		}
	}

	httpServer := &http.Server{
		Handler: application.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.Serve(listener) }()
	log.Printf("OwnGit listening on %s", listener.Addr())

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		_ = httpServer.Close()
		if waitErr := gitHandler.Wait(context.Background()); waitErr != nil {
			return waitErr
		}
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-signalContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		shutdownErr := httpServer.Shutdown(shutdownContext)
		cancel()
		if shutdownErr != nil {
			_ = httpServer.Close()
		}
		if err := gitHandler.Wait(context.Background()); err != nil {
			return fmt.Errorf("wait for Git process cleanup: %w", err)
		}
		if shutdownErr != nil {
			return fmt.Errorf("graceful shutdown: %w", shutdownErr)
		}
		return nil
	}
}

func setupLink(arguments []string) error {
	flags := flag.NewFlagSet("setup-link", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	baseURL := flags.String("base-url", "http://127.0.0.1:7654", "owner-facing HTTP origin")
	noOpen := flags.Bool("no-open", false, "do not open the private setup file")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	store, err := state.Open(context.Background(), *stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	settings, err := store.Settings(context.Background())
	if err != nil {
		return err
	}
	if settings.Initialized {
		return errors.New("setup is already complete")
	}
	path, err := (&bootstrap.Issuer{Store: store, BaseURL: *baseURL}).Issue(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("Owner setup file written to %s\n", path)
	if !*noOpen {
		return bootstrap.Open(path)
	}
	return nil
}

func resetAdmin(arguments []string) error {
	flags := flag.NewFlagSet("reset-admin", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	passwordFile := flags.String("password-file", "", "owner-readable file containing the new password")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *passwordFile == "" {
		return errors.New("--password-file is required; passwords are never accepted as command arguments")
	}
	password, err := readPrivatePassword(*passwordFile)
	if err != nil {
		return err
	}
	encoded, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	store, err := state.Open(context.Background(), *stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	settings, err := store.Settings(context.Background())
	if err != nil {
		return err
	}
	if !settings.Initialized {
		return errors.New("setup is not complete")
	}
	accessHash, err := store.PasswordHash(context.Background(), "access")
	if err != nil {
		return err
	}
	if accessHash != "" && auth.CheckPassword(accessHash, password) {
		return errors.New("administrator password must differ from the shared access password")
	}
	if err := store.SetAdminPassword(context.Background(), encoded); err != nil {
		return err
	}
	fmt.Println("Administrator password reset. Repository data was not changed; existing administrator sessions were revoked.")
	return nil
}

func approveHost(arguments []string) error {
	flags := flag.NewFlagSet("approve-host", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("approve-host requires exactly one host name")
	}
	host := flags.Arg(0)
	policy := server.NewHostPolicy()
	if err := policy.Add(host); err != nil {
		return err
	}
	store, err := state.Open(context.Background(), *stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.AddTrustedHost(context.Background(), host); err != nil {
		return err
	}
	fmt.Println("Host approved. Restart the server to load the change.")
	return nil
}

func backupState(arguments []string) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	output := flags.String("output", "", "new backup directory")
	gitPath := flags.String("git", "", "Git executable path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *output == "" {
		return errors.New("backup requires --output and accepts no positional arguments")
	}
	if err := state.RequireExisting(*stateDir); err != nil {
		return err
	}
	unlock, err := state.AcquireOfflineLock(*stateDir)
	if err != nil {
		return fmt.Errorf("backup requires OwnGit to be offline: %w", err)
	}
	defer unlock()
	store, err := state.Open(context.Background(), *stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	settings, err := store.Settings(context.Background())
	if err != nil {
		return err
	}
	if !settings.Initialized {
		return errors.New("setup is not complete")
	}
	runner, err := gitexec.New(*gitPath, filepath.Join(store.Dir(), "runtime"))
	if err != nil {
		return err
	}
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: settings.RepositoryRoot}
	if err := recovery.Create(context.Background(), store, manager, *output); err != nil {
		return err
	}
	fmt.Printf("Offline backup written to %s. SHA-256 hashes detect corruption but do not authenticate a replaced backup.\n", *output)
	return nil
}

func restoreState(arguments []string) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "new host-local state directory")
	input := flags.String("input", "", "offline backup directory")
	repositoryRoot := flags.String("repository-root", "", "new repository storage directory")
	gitPath := flags.String("git", "", "Git executable path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *input == "" || *repositoryRoot == "" {
		return errors.New("restore requires --input and --repository-root and accepts no positional arguments")
	}
	if err := recovery.Restore(context.Background(), *input, *stateDir, *repositoryRoot, *gitPath); err != nil {
		return err
	}
	fmt.Printf("Offline backup restored to %s with repositories at %s. Previous sessions and trusted hosts were not restored.\n", *stateDir, *repositoryRoot)
	return nil
}

func readPrivatePassword(path string) (string, error) {
	if err := state.ValidatePrivateFile(path); err != nil {
		return "", fmt.Errorf("inspect password file: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, 1026))
	if err != nil {
		return "", err
	}
	if len(content) > 1025 {
		return "", errors.New("password file is too large")
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(content), "\n"), "\r")
	if err := auth.ValidatePassword(password); err != nil {
		return "", err
	}
	return password, nil
}

func ownerOrigin(configured, listenAddress string) (string, error) {
	if configured == "" {
		host, port, err := net.SplitHostPort(listenAddress)
		if err != nil {
			return "", fmt.Errorf("invalid listen address: %w", err)
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		configured = "http://" + net.JoinHostPort(host, port)
	}
	parsed, err := url.Parse(configured)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("--base-url must be an HTTP(S) origin without a path, query, credentials, or fragment")
	}
	return parsed.String(), nil
}

func defaultStateDir() string {
	if configured, err := os.UserConfigDir(); err == nil {
		return defaultStatePath(configured, "")
	}
	home, _ := os.UserHomeDir()
	return defaultStatePath("", home)
}

func defaultStatePath(configured, home string) string {
	if configured != "" {
		return filepath.Join(configured, "owngit")
	}
	return filepath.Join(home, ".owngit")
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit [serve|setup-link|reset-admin|approve-host|backup|restore|pr] [options]")
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}
