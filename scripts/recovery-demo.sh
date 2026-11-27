#!/usr/bin/env bash
set -euo pipefail

: "${DATABASE_URL:?Set DATABASE_URL to a fresh demo database}"
export RPC_URL="${RPC_URL:-https://ethereum-sepolia-rpc.publicnode.com}"
cd "$(dirname "$0")/.."
sql() { psql "$DATABASE_URL" -XAtq -v ON_ERROR_STOP=1 -c "$1"; }
rpc() {
  curl -fsS --max-time 25 -H 'Content-Type: application/json' --data "$1" "$RPC_URL" |
    jq -ce 'if .error then error(.error.message) else .result end'
}
head_block() {
  local hex
  hex=$(rpc '{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["finalized",false]}' | jq -er .number) || return 1
  printf '%d\n' "$hex"
}
checkpoint() { sql 'SELECT next_block FROM sepolia_usdc_checkpoint'; }
snapshot() {
  sql 'SELECT block_number,log_index,tx_hash,sender,recipient,amount_raw FROM sepolia_usdc_transfers ORDER BY block_number,log_index'
  sql 'SELECT last_from,next_block FROM sepolia_usdc_checkpoint'
}

if [[ $# -gt 1 || ( $# -eq 1 && $1 != --wait-for-finality ) ]]; then
  echo 'Usage: bash scripts/recovery-demo.sh [--wait-for-finality]' >&2
  exit 2
fi
if [[ $(sql "SELECT count(*) FROM (VALUES (to_regclass('sepolia_usdc_checkpoint')), (to_regclass('sepolia_usdc_transfers'))) AS t(name) WHERE name IS NOT NULL") != 0 ]]; then
  echo 'Use a fresh database for the demo.' >&2
  exit 1
fi
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
go build -o "$work/indexer" ./cmd/chain-event-indexer
head=$(head_block)
start=$((head-63))
"$work/indexer" -once -start-block "$start"
next=$(checkpoint)
[[ $next -eq $((start+32)) ]]
echo "First process exited. Next block is $next."

if [[ ${1:-} == --wait-for-finality ]]; then
  echo "Waiting for a new finalized block after $head while the indexer is offline..."
  deadline=$((SECONDS+1800))
  current=$(head_block)
  while (( current <= head )); do
    if (( SECONDS >= deadline )); then echo "Finalized head did not advance within 30 minutes" >&2; exit 1; fi
    sleep 12
    current=$(head_block)
  done
fi
target=$(head_block)
while (( next <= target )); do
  "$work/indexer" -once
  previous=$next
  next=$(checkpoint)
  [[ $next -gt $previous ]]
done
if [[ $(sql 'SELECT count(*) FROM sepolia_usdc_transfers WHERE block_number >= (SELECT last_from FROM sepolia_usdc_checkpoint)') == 0 ]]; then
  echo 'The last batch is empty. Try a fresh window to demonstrate duplicate suppression with actual events.' >&2
  exit 1
fi
snapshot > "$work/before"
"$work/indexer" -replay-last
snapshot > "$work/after"
diff -u "$work/before" "$work/after"

# Read the same interval independently, using different RPC batch boundaries.
last=$((next-1))
for ((lo=start; lo<=last; lo+=31)); do
  hi=$((lo+30))
  if ((hi>last)); then hi=$last; fi
  request=$(printf '{"jsonrpc":"2.0","id":1,"method":"eth_getLogs","params":[{"fromBlock":"0x%x","toBlock":"0x%x","address":"0x1c7d4b196cb0c7b01d743fbc6116a902379c7238","topics":["0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"]}]}' "$lo" "$hi")
  rpc "$request" | jq -r '.[] | (.transactionHash|ascii_downcase) + ":" + .logIndex' >> "$work/rpc"
done
LC_ALL=C sort "$work/rpc" > "$work/expected"
sql "SELECT tx_hash || ':0x' || to_hex(log_index) FROM sepolia_usdc_transfers WHERE block_number BETWEEN $start AND $last" |
  LC_ALL=C sort > "$work/stored"
diff -u "$work/expected" "$work/stored"
count=$(sql 'SELECT count(*) FROM sepolia_usdc_transfers')
echo "Verified $count event IDs across blocks $start..$last. Rows and checkpoint are identical after replay."
