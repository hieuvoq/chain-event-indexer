package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	start := flag.Int64("start-block", -1, "first block on a fresh database (default: latest 64 finalized blocks)")
	once := flag.Bool("once", false, "process at most one finalized batch and exit")
	replay := flag.Bool("replay-last", false, "reprocess the last committed batch and exit")
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	flag.Parse()
	if *start < -1 || (*once && *replay) || flag.NArg() != 0 {
		return fmt.Errorf("invalid arguments: use -help for options")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("set DATABASE_URL to a PostgreSQL connection string")
	}
	source := rpc(os.Getenv("RPC_URL"))
	if source == "" {
		source = defaultRPC
	}
	var chain string
	if err := source.call(ctx, "eth_chainId", []any{}, &chain); err != nil {
		return err
	}
	if chain != "0xaa36a7" {
		return fmt.Errorf("expected Ethereum Sepolia (0xaa36a7), received %q", chain)
	}
	first := *start
	if first == -1 {
		head, err := source.finalized(ctx)
		if err != nil {
			return err
		}
		first = max(0, head-2*batchSize+1)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := prepare(ctx, db, first); err != nil {
		return err
	}
	if *once || *replay {
		_, err := batch(ctx, db, source, *replay)
		return err
	}

	server := &http.Server{Addr: *listen, Handler: eventsAPI(db), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	defer server.Close()
	log.Printf("serving GET /events on %s", *listen)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-serverErr:
			return err
		default:
		}
		progress, err := batch(ctx, db, source, false)
		if err != nil && ctx.Err() == nil {
			log.Printf("batch failed, retrying: %v", err)
		}
		if progress {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-serverErr:
			return err
		case <-time.After(10 * time.Second):
		}
	}
}
