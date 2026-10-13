// Command sighup rolls one or more Kubernetes Deployments whenever their container tags' digests
// move in the registry.
//
// Two modes:
//
//	serve    receive Harbor webhooks AND poll on an interval (a resident Deployment)
//	cronjob  check every target and exit; run it from a CronJob for scheduled checks
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
		fmt.Fprintf(fs.Output(), "usage: sighup [flags] (serve|cronjob)\n\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(os.Args[1:])

	mode, err := resolveMode(fs.Args())
	if err != nil {
		return err
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

	if mode == "cronjob" {
		return runOnce(ctx, engine, log)
	}
	return engine.Serve(ctx)
}

// resolveMode reads the mode from the positional argument: `serve` (the default, resident) or
// `cronjob` (scheduled). Anything else is an error, so a typo fails loudly instead of silently
// starting a resident server.
func resolveMode(args []string) (string, error) {
	switch {
	case len(args) == 0:
		return "serve", nil
	case args[0] == "serve" || args[0] == "cronjob":
		return args[0], nil
	default:
		return "", fmt.Errorf("unknown mode %q: want serve or cronjob", args[0])
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
