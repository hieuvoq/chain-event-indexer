# Chain Event Indexer

Chain Event Indexer is a backend project for turning on-chain events into records an application can query over HTTP. Its first milestone is reliable recovery after downtime.

## Planned first release

- Follow one event type on a single testnet.
- Store each event in PostgreSQL under a unique chain event ID so replaying a batch is safe.
- Commit each batch and its checkpoint in the same database transaction. After a restart, resume from the last committed checkpoint.
- Expose a paginated HTTP endpoint for reading indexed events in chain order.

## Recovery demo

The first release should pass this experiment:

1. Start the indexer and record its latest checkpoint.
2. Stop it while the testnet continues producing events.
3. Restart it and let it catch up. Replay the last batch once more.
4. Compare the stored event IDs with the chain over the same range. Every matching event should appear exactly once, and replaying the batch should leave the result unchanged.

## Status

Project design. The first implementation milestone is to choose the testnet and event type, then document how the chain identifies an event and when it is final enough to index.
