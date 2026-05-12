// kargo-mock-server is a fake Kargo API server for demos and screen
// recordings. It speaks the same Connect-RPC surface kargo-tui talks to
// (`/akuity.io.kargo.service.v1alpha1.KargoService/<Method>`), backed by
// in-memory state built from a small topology fixture plus a procedural
// volume generator. Promote RPCs actually mutate state and drive a
// WatchStages broadcaster, so the TUI's graph view animates in real time
// when the user hits `p`.
//
// No auth, no TLS. Point the TUI at http://localhost:8080 and go.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"
)

func main() {
	cmd := &cli.Command{
		Name:  "kargo-mock-server",
		Usage: "Fake Kargo API server backed by procedurally generated demo data",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "addr",
				Value: ":8080",
				Usage: "TCP address to listen on",
			},
			&cli.Int64Flag{
				Name:  "seed",
				Value: 42,
				Usage: "Seed for the procedural volume generator (changes every name and timestamp)",
			},
			&cli.Float64Flag{
				Name:  "speed",
				Value: 1.0,
				Usage: "Speed multiplier for promotion cascades and background motion (higher = faster)",
			},
			&cli.StringFlag{
				Name:  "fixtures-dir",
				Value: "examples/mock",
				Usage: "Directory holding the three topology YAMLs",
			},
		},
		Action: run,
	}
	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd *cli.Command) error {
	addr := cmd.String("addr")
	seed := cmd.Int64("seed")
	speed := cmd.Float64("speed")
	fixturesDir := cmd.String("fixtures-dir")

	store, err := bootstrap(fixturesDir, seed)
	if err != nil {
		return fmt.Errorf("bootstrap state: %w", err)
	}

	startMotion(ctx, store, speed)

	mux := http.NewServeMux()
	registerRoutes(mux, store, speed)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	shutdownCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("kargo-mock-server listening on %s\n", addr)
		fmt.Printf("  projects: %s\n", store.projectSummary())
		fmt.Printf("  seed=%d speed=%.1fx\n", seed, speed)
		fmt.Printf("\npoint the TUI at:\n  kargo-tui auth login http://localhost%s --name demo --insecure-skip-tls-verify\n  kargo-tui --context demo\n", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-shutdownCtx.Done():
		fmt.Println("\nshutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
