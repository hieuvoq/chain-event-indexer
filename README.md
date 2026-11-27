# Chain Event Indexer

A Go service that indexes USDC transfers on Ethereum Sepolia into PostgreSQL and serves them through a paginated HTTP API. You can stop the process, let the chain advance, then restart it against the same database. The recovery demo checks every stored event ID against a fresh RPC query.

## Run locally

Use Go 1.22 or later and PostgreSQL. To start a local PostgreSQL 16 instance with a persistent volume:

```sh
docker run -d --name chain-indexer-db \
  -p 127.0.0.1:55432:5432 -v chain-indexer-data:/var/lib/postgresql/data \
  -e POSTGRES_PASSWORD=indexer-demo -e POSTGRES_DB=indexer postgres:16-alpine
export DATABASE_URL='postgres://postgres:indexer-demo@127.0.0.1:55432/indexer?sslmode=disable'
go build -o chain-event-indexer .
./chain-event-indexer
```

Wait for PostgreSQL to accept connections before starting the service. On a fresh database the indexer starts 63 blocks behind the finalized head. To choose the first block yourself, pass `-start-block N` on the first run. Subsequent runs use the stored checkpoint. The default RPC is `https://ethereum-sepolia-rpc.publicnode.com`. Set `RPC_URL` to use another Sepolia endpoint that supports `finalized` and `eth_getLogs`.

## Read transfers

```sh
curl -fsS 'http://127.0.0.1:8080/events?limit=1'
```

Example response from Sepolia:

```json
{
  "events": [{
    "block_number": 12221724,
    "log_index": 3,
    "transaction_hash": "0x558bb89c9c50601312d92f2f545a9a44b1f37eb9a6e7382409ba0be0fc9373b3",
    "from": "0xdaf29b0d05161176c980de486d32bda7243d7013",
    "to": "0xe963647d82e8935bc14a8e400ed11c4abdb94796",
    "amount_raw": "10"
  }],
  "next_cursor": "12221724:3"
}
```

Pass the returned cursor as `after`, for example `GET /events?limit=1&after=12221724:3`. Events are ordered by block number and log index. The default limit is 50 and the maximum is 100. A null cursor means that page reached the end of the currently indexed data. The amount is a decimal string in the token's smallest unit.

## Recovery demo

The demo uses Bash, Go, curl, jq, psql, and a fresh database. With the Docker instance above, run:

```sh
docker exec chain-indexer-db createdb -U postgres recovery_demo
DATABASE_URL='postgres://postgres:indexer-demo@127.0.0.1:55432/recovery_demo?sslmode=disable' \
  bash scripts/recovery-demo.sh --wait-for-finality
```

The first process indexes 32 blocks and exits. The script records its checkpoint and waits for Sepolia to finalize more blocks while the indexer is offline. New processes then catch up from the checkpoint. The script snapshots the rows and checkpoint, replays the last committed batch, and compares both snapshots. Finally, it queries the same block interval independently with different RPC batch boundaries and compares the full list of event IDs, including their counts.

Omit `--wait-for-finality` for a quick run against an existing backlog. Use a fresh database for each demo. To exercise the same operations manually, run `./chain-event-indexer -once` for one batch and `./chain-event-indexer -replay-last` to replay the last committed range.

### Recorded run on Sepolia

The first process exited with a checkpoint of `12221756`. While it was offline, the finalized head moved from `12221787` to `12221818`.

| Operation | Inclusive block range | Matching events | Rows inserted |
| --- | --- | ---: | ---: |
| First process | 12221724–12221755 | 319 | 319 |
| Resume from checkpoint | 12221756–12221787 | 159 | 159 |
| Catch up through the new head | 12221788–12221818 | 191 | 191 |
| Replay the last batch | 12221788–12221818 | 191 | 0 |

All 669 stored event IDs matched the independent RPC read. The full rows and the next checkpoint (`12221819`) were identical before and after replay.

## Why restart is safe

The worker checks the chain ID and reads at most 32 finalized blocks per batch. It filters for the ERC-20 `Transfer` event from Circle's Sepolia USDC contract at `0x1c7d4b196cb0c7b01d743fbc6116a902379c7238`. Finality comes from the RPC provider's `finalized` block tag. [Circle contract registry](https://developers.circle.com/stablecoins/usdc-contract-addresses) · [Ethereum JSON-RPC](https://ethereum.org/developers/docs/apis/json-rpc/)

- A unique constraint on `(tx_hash, log_index)` identifies each event in this single-chain database. One transaction can emit several transfers.
- The worker locks the checkpoint row, inserts the events, and advances the checkpoint in one PostgreSQL transaction. A failed batch rolls back both changes. An empty batch still advances the checkpoint.
- On restart the worker begins at `next_block`. Explicit replay reads the saved inclusive range again. Existing event IDs are skipped. Concurrent workers serialize on the same checkpoint.

## Tests

```sh
go test ./...
TEST_DATABASE_URL="$DATABASE_URL" go test -race -v ./...
```

The PostgreSQL test creates and removes its own schema. It forces a failure on the second insert, checks rollback, retries and replays the batch, advances over an empty range, and reads two events from the same transaction on separate API pages.
