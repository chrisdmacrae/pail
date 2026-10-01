// pail-server is a Pail installation: the REST API and every pail's site on
// one listener, with storage on versitygw.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chrisdmacrae/pail/internal/config"
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
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("pail can't start", "err", err)
		os.Exit(1)
	}
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

	svc := pails.New(pails.Options{
		Store:           store,
		BaseDomain:      cfg.BaseDomain,
		MaxDeploys:      cfg.MaxDeploys,
		MaxUnpackedSize: cfg.MaxUploadSize * unpackedRatio,
		Logger:          logger,
	})
	if err := svc.Load(ctx); err != nil {
		return err
	}

	handler := server.New(cfg, svc, webui.FS(), logger, version)
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	logger.Info("pail is up", "listen", cfg.Listen, "base_domain", cfg.BaseDomain, "pails", len(svc.List()), "version", version)

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
	logger.Info("stopping: letting running deploys finish")
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	svc.Wait()
	return nil
}
