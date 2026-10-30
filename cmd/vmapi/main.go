// Command vmapi is the HTTP API server that manages VMs on libvirt.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/yuanying/vmapi/internal/command"
	"github.com/yuanying/vmapi/internal/config"
	"github.com/yuanying/vmapi/internal/image"
	"github.com/yuanying/vmapi/internal/libvirt"
	"github.com/yuanying/vmapi/internal/server"
	"github.com/yuanying/vmapi/internal/vm"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vmapi", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the version and exit")
	configPath := fs.String("config", "", "the configuration file (default $XDG_CONFIG_HOME/vmapi/config.yaml or ~/.config/vmapi/config.yaml)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintf(stdout, "vmapi %s\n", version)
		return 0
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	path := *configPath
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "vmapi: %v\n", err)
		return 1
	}

	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, cfg, logger); err != nil {
		logger.Error("vmapi stopped", "error", err)
		return 1
	}
	return 0
}

func serve(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	hv := libvirt.New(cfg.Libvirt.URI, cfg.Libvirt.Socket)
	defer func() { _ = hv.Close() }()
	runner := command.Exec{}
	svc := &vm.Service{
		Config:     cfg,
		Hypervisor: hv,
		ImageStore: &image.Store{Dir: filepath.Join(cfg.Storage.Dir, "images"), Client: http.DefaultClient},
		Disks:      command.Disks{Runner: runner},
		Seeds:      command.Seeds{Runner: runner},
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           (&server.Server{Service: svc, Logger: logger}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: creating the first VM from an image waits for
		// the image to download (docs/adr/0001).
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	logger.Info("vmapi started", "version", version, "listen", cfg.Listen, "libvirt", cfg.Libvirt.URI)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	logger.Info("vmapi stopping")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
