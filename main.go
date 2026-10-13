// Command sighup rolls a Kubernetes Deployment whenever a container tag's digest moves in
// the registry.
//
// Two modes:
//
//	serve  receive Harbor webhooks AND poll on an interval (a resident Deployment)
//	once   run a single check and exit (a CronJob)
//
// The poll loop is not optional in `serve`: Harbor retries a failed webhook only three times with
// a 3s client timeout, so a missed delivery would otherwise leave the Deployment stale forever.
package main

import (
	"context"
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

	r, err := roller.New(cfg, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch mode {
	case "once":
		res, err := r.RunOnce(ctx)
		if err != nil {
			return err
		}
		log.Info("check complete",
			"deployment", res.Deployment,
			"tag", res.Tag,
			"applied", res.Applied,
			"digest", res.Digest,
			"rolled", res.Rolled,
			"reason", res.Reason,
		)
		return nil
	case "serve":
		return r.Serve(ctx)
	default:
		return fmt.Errorf("unknown mode %q: want serve or once", mode)
	}
}
