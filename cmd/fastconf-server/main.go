// Command fastconf-server serves loaded configuration over HTTP.
//
// It loads configuration once at startup, fails fast on a load error and
// serves the configuration root, tenant configurations, value lookups and an
// explicit reload endpoint. Send SIGHUP to reload configuration in place.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/edanko/fastconf"
	"github.com/edanko/fastconf/internal/configapi"
	"github.com/edanko/fastconf/internal/webui"
)

func main() {
	if err := run(os.Args[1:], os.Getenv); err != nil {
		slog.Error("fastconf-server: " + err.Error())
		os.Exit(1)
	}
}

func run(arguments []string, getenv func(string) string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	flags := flag.NewFlagSet("fastconf-server", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var configPath, bootstrapPath string
	fastconf.BindFlags(flags, &configPath, &bootstrapPath)
	application := flags.String("app", "", "application name")
	subsystem := flags.String("subsystem", "", "subsystem name")
	environment := flags.String("environment", "", "environment name")
	listen := flags.String("listen", "127.0.0.1:8080", "HTTP listen address")
	token := flags.String("token", getenv("FASTCONF_SERVER_TOKEN"), "optional bearer token required from clients")
	ui := flags.Bool("ui", true, "serve the web console at /ui/")
	if err := flags.Parse(arguments); err != nil {
		return err
	}

	bootstrapSet := false
	flags.Visit(func(flag *flag.Flag) {
		if flag.Name == "bootstrap_path" {
			bootstrapSet = true
		}
	})

	options := []fastconf.Option{
		fastconf.WithConfigPath(configPath),
		fastconf.WithLogger(logger),
	}
	if *application != "" {
		options = append(options, fastconf.WithApplication(*application))
	}
	if *subsystem != "" {
		options = append(options, fastconf.WithSubsystem(*subsystem))
	}
	if *environment != "" {
		options = append(options, fastconf.WithEnvironment(*environment))
	}
	if bootstrapSet {
		options = append(options, fastconf.WithBootstrapPath(bootstrapPath))
	}

	loader, err := fastconf.New(options...)
	if err != nil {
		return fmt.Errorf("create loader: %w", err)
	}
	defer func() { _ = loader.Close() }()

	// Load before binding the listener so a broken configuration fails fast.
	if _, err := loader.Map(context.Background()); err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	configHandler := configapi.New(loader,
		configapi.WithLogger(logger),
		configapi.WithToken(*token),
	)
	handler := configHandler
	if *ui {
		console, err := webui.New(loader,
			webui.WithLogger(logger),
			webui.WithToken(*token),
		)
		if err != nil {
			return fmt.Errorf("create web console: %w", err)
		}
		mux := http.NewServeMux()
		mux.Handle("/ui/", console)
		mux.Handle("/ui", http.RedirectHandler("/ui/", http.StatusFound))
		mux.Handle("/", configHandler)
		handler = mux
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", *listen)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", *listen, err)
	}
	logger.Info("fastconf-server: listening",
		"address", listener.Addr().String(),
		"config_path", configPath,
		"application", *application,
		"subsystem", *subsystem,
		"environment", *environment,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// SIGHUP reloads the configuration without dropping connections.
	hangups := make(chan os.Signal, 1)
	signal.Notify(hangups, syscall.SIGHUP)
	defer signal.Stop(hangups)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hangups:
				if err := loader.Reload(context.Background()); err != nil {
					logger.Error("fastconf-server: reload failed", "error", err)
					continue
				}
				logger.Info("fastconf-server: configuration reloaded")
			}
		}
	}()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	logger.Info("fastconf-server: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
