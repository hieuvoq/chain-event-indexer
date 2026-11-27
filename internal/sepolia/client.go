package sepolia

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRPC    = "https://ethereum-sepolia-rpc.publicnode.com"
	usdc          = "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238"
	transferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
)

var (
	rpcClient    = http.Client{Timeout: 20 * time.Second}
	word         = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
	addressTopic = regexp.MustCompile(`^0x0{24}[0-9a-fA-F]{40}$`)
)

type Client struct {
	endpoint string
}

func NewClient(endpoint string) *Client {
	if endpoint == "" {
		endpoint = defaultRPC
	}
	return &Client{endpoint: endpoint}
}

func (r *Client) CheckChain(ctx context.Context) error {
	var chain string
	if err := r.call(ctx, "eth_chainId", []any{}, &chain); err != nil {
		return err
	}
	if chain != "0xaa36a7" {
		return fmt.Errorf("expected Ethereum Sepolia (0xaa36a7), received %q", chain)
	}
	return nil
}

func (r *Client) call(ctx context.Context, method string, params []any, target any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := rpcClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %s", method, resp.Status)
	}
	var reply struct {
		ID     int
		Result json.RawMessage
		Error  *struct {
			Code    int
			Message string
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return err
	}
	if reply.Error != nil {
		return fmt.Errorf("%s: RPC %d: %s", method, reply.Error.Code, reply.Error.Message)
	}
	if reply.ID != 1 || len(reply.Result) == 0 || string(reply.Result) == "null" {
		return fmt.Errorf("%s: missing result or wrong response ID", method)
	}
	return json.Unmarshal(reply.Result, target)
}

func quantity(s string) (int64, error) {
	if !strings.HasPrefix(s, "0x") {
		return 0, fmt.Errorf("invalid RPC quantity %q", s)
	}
	n, err := strconv.ParseUint(s[2:], 16, 63)
	return int64(n), err
}

func (r *Client) Finalized(ctx context.Context) (int64, error) {
	var block struct{ Number string }
	if err := r.call(ctx, "eth_getBlockByNumber", []any{"finalized", false}, &block); err != nil {
		return 0, err
	}
	return quantity(block.Number)
}

type Transfer struct {
	Block  int64  `json:"block_number"`
	Index  int64  `json:"log_index"`
	Tx     string `json:"transaction_hash"`
	From   string `json:"from"`
	To     string `json:"to"`
	Amount string `json:"amount_raw"`
}

type rpcLog struct {
	BlockNumber, LogIndex, TransactionHash, Address, Data string
	Topics                                                []string
	Removed                                               bool
}

func (l rpcLog) decode(first, last int64) (Transfer, error) {
	block, err := quantity(l.BlockNumber)
	if err != nil || block < first || block > last {
		return Transfer{}, fmt.Errorf("unexpected block %q in range %d..%d", l.BlockNumber, first, last)
	}
	index, err := quantity(l.LogIndex)
	if err != nil {
		return Transfer{}, err
	}
	if l.Removed || !strings.EqualFold(l.Address, usdc) || !word.MatchString(l.TransactionHash) || !word.MatchString(l.Data) ||
		len(l.Topics) != 3 || !strings.EqualFold(l.Topics[0], transferTopic) || !addressTopic.MatchString(l.Topics[1]) || !addressTopic.MatchString(l.Topics[2]) {
		return Transfer{}, fmt.Errorf("invalid finalized USDC Transfer at %d:%d", block, index)
	}
	amount, _ := new(big.Int).SetString(l.Data[2:], 16)
	return Transfer{
		Block: block, Index: index, Tx: strings.ToLower(l.TransactionHash),
		From:   "0x" + strings.ToLower(l.Topics[1][26:]),
		To:     "0x" + strings.ToLower(l.Topics[2][26:]),
		Amount: amount.String(),
	}, nil
}

func (r *Client) Transfers(ctx context.Context, first, last int64) ([]Transfer, error) {
	filter := map[string]any{
		"fromBlock": fmt.Sprintf("0x%x", first), "toBlock": fmt.Sprintf("0x%x", last),
		"address": usdc, "topics": []string{transferTopic},
	}
	var logs []rpcLog
	if err := r.call(ctx, "eth_getLogs", []any{filter}, &logs); err != nil {
		return nil, err
	}
	events := make([]Transfer, 0, len(logs))
	for _, l := range logs {
		e, err := l.decode(first, last)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}
