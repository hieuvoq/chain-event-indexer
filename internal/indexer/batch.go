package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/hieuvoq/chain-event-indexer/internal/postgres"
	"github.com/hieuvoq/chain-event-indexer/internal/sepolia"
)

const BatchSize int64 = 32

func RunBatch(ctx context.Context, db *sql.DB, source *sepolia.Client, replay bool) (bool, error) {
	// The row lock serializes workers and replay. The RPC call has a bounded timeout.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	next, previous, err := postgres.LockCheckpoint(ctx, tx)
	if err != nil {
		return false, err
	}
	first, last := next, next-1
	if replay {
		if !previous.Valid {
			return false, fmt.Errorf("no committed batch to replay")
		}
		first = previous.Int64
	} else {
		head, err := source.Finalized(ctx)
		if err != nil {
			return false, err
		}
		if next > head {
			return false, nil
		}
		last = min(head, next+BatchSize-1)
	}
	events, err := source.Transfers(ctx, first, last)
	if err != nil {
		return false, err
	}
	inserted, err := postgres.InsertTransfers(ctx, tx, events)
	if err != nil {
		return false, err
	}
	if !replay {
		if err = postgres.AdvanceCheckpoint(ctx, tx, first, last); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	log.Printf("blocks=%d..%d matched=%d inserted=%d replay=%t", first, last, len(events), inserted, replay)
	return true, nil
}
