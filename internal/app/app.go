package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hieuvoq/chain-event-indexer/internal/httpapi"
	"github.com/hieuvoq/chain-event-indexer/internal/indexer"
	"github.com/hieuvoq/chain-event-indexer/internal/postgres"
	"github.com/hieuvoq/chain-event-indexer/internal/sepolia"
)

func Main() {
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
	source := sepolia.NewClient(os.Getenv("RPC_URL"))
	if err := source.CheckChain(ctx); err != nil {
		return err
	}
	first := *start
	if first == -1 {
		head, err := source.Finalized(ctx)
		if err != nil {
			return err
		}
		first = max(0, head-2*indexer.BatchSize+1)
	}
	db, err := postgres.Open(dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := postgres.Prepare(ctx, db, first); err != nil {
		return err
	}
	if *once || *replay {
		_, err := indexer.RunBatch(ctx, db, source, *replay)
		return err
	}

	server := &http.Server{Addr: *listen, Handler: httpapi.NewHandler(db), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
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
		progress, err := indexer.RunBatch(ctx, db, source, false)
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
