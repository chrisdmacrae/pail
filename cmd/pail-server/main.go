// pail-server is a Pail installation: the REST API and every pail's site on
// one listener, with storage on versitygw.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chrisdmacrae/pail/internal/certs"
	"github.com/chrisdmacrae/pail/internal/config"
	"github.com/chrisdmacrae/pail/internal/engine"
	"github.com/chrisdmacrae/pail/internal/githost"
	"github.com/chrisdmacrae/pail/internal/microvm"
	"github.com/chrisdmacrae/pail/internal/pails"
	"github.com/chrisdmacrae/pail/internal/server"
	"github.com/chrisdmacrae/pail/internal/storage"
	"github.com/chrisdmacrae/pail/internal/webui"
)

// version is set at release with -ldflags "-X main.version=...".
var version = "dev"

// unpackedRatio is how many times its own size an archive may unpack to.
const unpackedRatio = 10

func main() {
	// Inside a microVM, this same binary is what the kernel boots.
	if microvm.IsGuestInit() {
		microvm.GuestMain()
		return
	}
	// In a function's container, it is the agent that runs the function.
	if engine.IsAgent() {
		engine.AgentMain()
		return
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("pail can't start", "err", err)
		os.Exit(1)
	}
}

// machinesFor picks what this Pail runs builds, containers and functions in, and
// says which in the log.
func machinesFor(ctx context.Context, cfg config.Config, logger *slog.Logger) (microvm.Machines, error) {
	vms := microvm.New(microvm.Config{Firecracker: cfg.Firecracker, Kernel: cfg.Kernel, Dir: cfg.DataDir}, logger)
	containers := engine.New(engine.Config{Socket: cfg.ContainerSocket, Network: cfg.ContainerNetwork, Dir: cfg.DataDir}, logger)

	ok, why := vms.Available()
	if cfg.Runtime == "container" || (cfg.Runtime == "auto" && !ok) {
		if can, whyNot := containers.Available(); can {
			if err := containers.Prepare(ctx); err != nil {
				return nil, err
			}
			logger.Info("this pail can run containers: builds, containers and functions are on",
				"engine", containers.Kind(), "socket", cfg.ContainerSocket, "network", cfg.ContainerNetwork)
			return containers, nil
		} else if cfg.Runtime == "container" {
			logger.Info("this pail serves static files only: " + whyNot)
			return containers, nil
		}
	}
	if ok {
		logger.Info("this pail can run microVMs: builds and containers are on", "firecracker", cfg.Firecracker, "kernel", cfg.Kernel)
	} else {
		logger.Info("this pail serves static files only: " + why)
	}
	return vms, nil
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.NewS3(ctx, storage.S3Options{
		Endpoint:  cfg.S3.Endpoint,
		AccessKey: cfg.S3.AccessKey,
		SecretKey: cfg.S3.SecretKey,
		Bucket:    cfg.S3.Bucket,
		Region:    cfg.S3.Region,
	})
	if err != nil {
		return err
	}

	// Builds and server code run in microVMs, which need Linux with KVM, or
	// where there is none, in the containers of Docker or Podman. Without
	// either Pail still serves static files.
	machines, err := machinesFor(ctx, cfg, logger)
	if err != nil {
		return err
	}

	svc := pails.New(pails.Options{
		Store:              store,
		BaseDomain:         cfg.BaseDomain,
		MaxDeploys:         cfg.MaxDeploys,
		MaxUnpackedSize:    cfg.MaxUploadSize * unpackedRatio,
		Builder:            machines,
		Dir:                cfg.DataDir,
		MaxContainerMemory: cfg.MaxContainerMemory,
		MaxFunctionMemory:  cfg.MaxFunctionMemory,
		Logger:             logger,
	})
	if err := svc.Load(ctx); err != nil {
		return err
	}
	// Containers come back up by themselves; nothing waits for them.
	svc.Resume()
	defer svc.Close()

	git, err := githost.LoadConnections(ctx, store, githost.DefaultClient())
	if err != nil {
		return err
	}
	git.Apps = map[githost.Kind]githost.App{}
	for host, app := range cfg.OAuth {
		git.Apps[githost.Kind(host)] = githost.App{ClientID: app.ClientID, ClientSecret: app.ClientSecret, Server: app.Server}
	}
	opts := server.Options{Config: cfg, Pails: svc, UI: webui.FS(), Git: git, Logger: logger, Version: version}

	// Certificates: Pail's own authority, or Let's Encrypt when a DNS
	// provider is set. In Let's Encrypt mode a first start waits here for
	// the base domain's wildcard.
	var manager certs.Manager
	if !cfg.TLSOff {
		// A wildcard needs something to sit under: browsers won't accept one
		// for a single-label domain such as *.localhost.
		if !strings.Contains(cfg.BaseDomain, ".") {
			logger.Warn("browsers reject a wildcard certificate for a single-label base domain; use one like pail.lan, or set PAIL_TLS=off", "base_domain", cfg.BaseDomain)
		}
		var hosts []string
		for _, p := range svc.List() {
			hosts = append(hosts, p.Hosts...)
		}
		manager, err = certs.New(ctx, certs.Options{Store: store, BaseDomain: cfg.BaseDomain, ACME: cfg.ACME, Hosts: hosts, Logger: logger})
		if err != nil {
			return err
		}
		opts.Certs = manager
		go manager.Run(ctx)
	}
	handler := server.New(opts)

	// With HTTPS on, the plain listener only redirects, answers the DNS
	// self-check and hands out the root certificate.
	plain := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	servers := []*http.Server{plain}
	errc := make(chan error, 2)
	if manager != nil {
		plain.Handler = handler.Plain()
		secure := &http.Server{
			Addr:              cfg.ListenTLS,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			TLSConfig:         &tls.Config{GetCertificate: manager.GetCertificate, MinVersion: tls.VersionTLS12},
		}
		servers = append(servers, secure)
		go func() { errc <- secure.ListenAndServeTLS("", "") }()
	}
	go func() { errc <- plain.ListenAndServe() }()
	logger.Info("pail is up", "http", cfg.Listen, "https", map[bool]string{true: cfg.ListenTLS, false: "off"}[manager != nil],
		"tls", handler.TLSMode(), "base_domain", cfg.BaseDomain, "pails", len(svc.List()), "version", version)

	// The DNS self-check: every pail's name has to find its way back here.
	go func() {
		if c := handler.CheckBaseDomain(ctx); c.PointsHere {
			logger.Info("names under the base domain reach this pail", "names", c.Host)
		} else {
			logger.Warn("names under the base domain don't reach this pail yet; point them at this server on your DNS", "names", c.Host, "found", c.Detail)
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	logger.Info("stopping: letting running deploys finish, then shutting containers down")
	handler.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
	svc.Wait()
	return nil
}
