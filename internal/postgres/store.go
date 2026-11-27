package postgres

import (
	"context"
	"database/sql"
	_ "embed"

	_ "github.com/lib/pq"

	"github.com/hieuvoq/chain-event-indexer/internal/sepolia"
)

//go:embed schema.sql
var schema string

func Open(dsn string) (*sql.DB, error) {
	return sql.Open("postgres", dsn)
}

func Prepare(ctx context.Context, db *sql.DB, start int64) error {
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

// LockCheckpoint holds the stream position until the caller commits or rolls back.
func LockCheckpoint(ctx context.Context, tx *sql.Tx) (next int64, previous sql.NullInt64, err error) {
	err = tx.QueryRowContext(ctx, "SELECT next_block,last_from FROM sepolia_usdc_checkpoint WHERE singleton FOR UPDATE").Scan(&next, &previous)
	return
}

func InsertTransfers(ctx context.Context, tx *sql.Tx, events []sepolia.Transfer) (int64, error) {
	var inserted int64
	for _, e := range events {
		result, err := tx.ExecContext(ctx, `INSERT INTO sepolia_usdc_transfers
			(block_number,log_index,tx_hash,sender,recipient,amount_raw)
			VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(tx_hash,log_index) DO NOTHING`, e.Block, e.Index, e.Tx, e.From, e.To, e.Amount)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		inserted += n
	}
	return inserted, nil
}

func AdvanceCheckpoint(ctx context.Context, tx *sql.Tx, first, last int64) error {
	_, err := tx.ExecContext(ctx, "UPDATE sepolia_usdc_checkpoint SET next_block=$1,last_from=$2 WHERE singleton", last+1, first)
	return err
}

func ListTransfers(ctx context.Context, db *sql.DB, block, index int64, limit int) ([]sepolia.Transfer, error) {
	rows, err := db.QueryContext(ctx, `SELECT block_number,log_index,tx_hash,sender,recipient,amount_raw::text
			FROM sepolia_usdc_transfers WHERE (block_number,log_index)>($1,$2)
			ORDER BY block_number,log_index LIMIT $3`, block, index, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]sepolia.Transfer, 0, limit)
	for rows.Next() {
		var e sepolia.Transfer
		if err := rows.Scan(&e.Block, &e.Index, &e.Tx, &e.From, &e.To, &e.Amount); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
