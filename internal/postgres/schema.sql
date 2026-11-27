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
);
