// Command sighup rolls one or more Kubernetes Deployments whenever their container tags' digests
// move in the registry.
//
// Two modes:
//
//	serve  receive Harbor webhooks AND poll on an interval (a resident Deployment)
//	once   run a single check across every target and exit (a CronJob)
//
// The poll loop is not optional in `serve`: Harbor retries a failed webhook only three times with
// a 3s client timeout, so a missed delivery would otherwise leave a Deployment stale forever.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"git.vn.linuxguru.net/jblack/sighup/internal/roller"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sighup:", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("sighup", flag.ExitOnError)
	debug := fs.Bool("debug", false, "enable debug logging")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: sighup [flags] (serve|once)\n\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(os.Args[1:])

	mode := "serve"
	if fs.NArg() > 0 {
		mode = fs.Arg(0)
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	cfg, err := roller.ConfigFromEnv()
	if err != nil {
		return err
	}

	engine, err := roller.New(cfg, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch mode {
	case "once":
		return runOnce(ctx, engine, log)
	case "serve":
		return engine.Serve(ctx)
	default:
		return fmt.Errorf("unknown mode %q: want serve or once", mode)
	}
}

// runOnce checks every target, logs each outcome, and fails if any target did.
func runOnce(ctx context.Context, engine *roller.Engine, log *slog.Logger) error {
	var errs []error

	for _, res := range engine.RunOnce(ctx) {
		if res.Err != nil {
			log.Error("check failed", "target", res.Target, "error", res.Err)
			errs = append(errs, fmt.Errorf("%s: %w", res.Target, res.Err))
			continue
		}
		log.Info("check complete",
			"target", res.Target,
			"tag", res.Tag,
			"applied", res.Applied,
			"digest", res.Digest,
			"rolled", res.Rolled,
			"reason", res.Reason,
		)
	}

	return errors.Join(errs...)
}
