package sepolia

import (
	"fmt"
	"strings"
	"testing"
)

func logFixture(index, amount int) rpcLog {
	return rpcLog{
		BlockNumber: "0x64", LogIndex: fmt.Sprintf("0x%x", index),
		TransactionHash: "0x" + strings.Repeat("a", 64), Address: usdc,
		Data:   fmt.Sprintf("0x%064x", amount),
		Topics: []string{transferTopic, "0x" + strings.Repeat("0", 64), "0x" + strings.Repeat("0", 24) + strings.Repeat("1", 40)},
	}
}

func TestDecodeTransfer(t *testing.T) {
	l := logFixture(3, 7)
	l.Data = "0x" + strings.Repeat("f", 64)
	e, err := l.decode(100, 131)
	if err != nil || e.Block != 100 || e.Index != 3 || e.To != "0x"+strings.Repeat("1", 40) || e.Amount != "115792089237316195423570985008687907853269984665640564039457584007913129639935" {
		t.Fatalf("decoded %+v: %v", e, err)
	}
	l.Removed = true
	if _, err := l.decode(100, 131); err == nil {
		t.Fatal("accepted removed event")
	}
}
