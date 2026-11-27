package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
)

const batchSize int64 = 32

const schema = `
CREATE TABLE IF NOT EXISTS sepolia_usdc_checkpoint (
	singleton boolean PRIMARY KEY CHECK (singleton),
	next_block bigint NOT NULL CHECK (next_block >= 0),
	last_from bigint CHECK (last_from >= 0 AND last_from < next_block)
);
CREATE TABLE IF NOT EXISTS sepolia_usdc_transfers (
	block_number bigint NOT NULL,
	log_index bigint NOT NULL,
	tx_hash text NOT NULL,
	sender text NOT NULL,
	recipient text NOT NULL,
	amount_raw numeric(78,0) NOT NULL,
	PRIMARY KEY (block_number, log_index),
	UNIQUE (tx_hash, log_index)
);`

func prepare(ctx context.Context, db *sql.DB, start int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialize first-time table creation when two workers start together.
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(11155111, 7238)"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sepolia_usdc_checkpoint(singleton,next_block)
		VALUES(true,$1) ON CONFLICT(singleton) DO NOTHING`, start); err != nil {
		return err
	}
	return tx.Commit()
}

func batch(ctx context.Context, db *sql.DB, source rpc, replay bool) (bool, error) {
	// The row lock serializes workers and replay. The RPC call has a bounded timeout.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var next int64
	var previous sql.NullInt64
	if err = tx.QueryRowContext(ctx, "SELECT next_block,last_from FROM sepolia_usdc_checkpoint WHERE singleton FOR UPDATE").Scan(&next, &previous); err != nil {
		return false, err
	}
	first, last := next, next-1
	if replay {
		if !previous.Valid {
			return false, fmt.Errorf("no committed batch to replay")
		}
		first = previous.Int64
	} else {
		head, err := source.finalized(ctx)
		if err != nil {
			return false, err
		}
		if next > head {
			return false, nil
		}
		last = min(head, next+batchSize-1)
	}
	events, err := source.transfers(ctx, first, last)
	if err != nil {
		return false, err
	}
	var inserted int64
	for _, e := range events {
		result, err := tx.ExecContext(ctx, `INSERT INTO sepolia_usdc_transfers
			(block_number,log_index,tx_hash,sender,recipient,amount_raw)
			VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(tx_hash,log_index) DO NOTHING`, e.Block, e.Index, e.Tx, e.From, e.To, e.Amount)
		if err != nil {
			return false, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		inserted += n
	}
	if !replay {
		if _, err = tx.ExecContext(ctx, "UPDATE sepolia_usdc_checkpoint SET next_block=$1,last_from=$2 WHERE singleton", last+1, first); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	log.Printf("blocks=%d..%d matched=%d inserted=%d replay=%t", first, last, len(events), inserted, replay)
	return true, nil
}
