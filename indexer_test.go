package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
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

func TestRecovery(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL integration test")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("indexer_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec("CREATE SCHEMA " + name); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec("DROP SCHEMA " + name + " CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	query := parsed.Query()
	query.Set("search_path", name)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("postgres", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := prepare(ctx, db, 100); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string
			Params []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var result any
		switch req.Method {
		case "eth_getBlockByNumber":
			result = map[string]string{"number": "0xa3"}
		case "eth_getLogs":
			var filter struct{ FromBlock string }
			if err := json.Unmarshal(req.Params[0], &filter); err != nil {
				t.Error(err)
				return
			}
			switch filter.FromBlock {
			case "0x64":
				result = []rpcLog{logFixture(3, 7), logFixture(4, 100)}
			case "0x84":
				result = []rpcLog{}
			default:
				t.Errorf("unexpected range starting at %s", filter.FromBlock)
				return
			}
		default:
			t.Errorf("unexpected method %s", req.Method)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	source := rpc(server.URL)
	assertState := func(next int64, count int) {
		t.Helper()
		var actual int64
		var rows int
		if err := db.QueryRow(`SELECT next_block,(SELECT count(*) FROM sepolia_usdc_transfers) FROM sepolia_usdc_checkpoint`).Scan(&actual, &rows); err != nil {
			t.Fatal(err)
		}
		if actual != next || rows != count {
			t.Fatalf("next=%d rows=%d; want %d and %d", actual, rows, next, count)
		}
	}
	// Make the second INSERT fail after the first succeeds inside the same transaction.
	if _, err := db.Exec("ALTER TABLE sepolia_usdc_transfers ADD CONSTRAINT test_failure CHECK(amount_raw < 100)"); err != nil {
		t.Fatal(err)
	}
	if _, err := batch(ctx, db, source, false); err == nil {
		t.Fatal("expected write failure")
	}
	assertState(100, 0)
	if _, err := db.Exec("ALTER TABLE sepolia_usdc_transfers DROP CONSTRAINT test_failure"); err != nil {
		t.Fatal(err)
	}
	if _, err := batch(ctx, db, source, false); err != nil {
		t.Fatal(err)
	}
	if err := prepare(ctx, db, 999); err != nil {
		t.Fatal(err)
	}
	if _, err := batch(ctx, db, source, true); err != nil {
		t.Fatal(err)
	}
	assertState(132, 2)
	if _, err := batch(ctx, db, source, false); err != nil {
		t.Fatal(err)
	}
	assertState(164, 2)

	handler := eventsAPI(db)
	for _, page := range []struct {
		path string
		want int64
		next string
	}{
		{"/events?limit=1", 3, "100:3"}, {"/events?limit=1&after=100:3", 4, ""},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", page.path, nil))
		var parsed struct {
			Events []transfer
			Next   string `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 200 || len(parsed.Events) != 1 || parsed.Events[0].Index != page.want || parsed.Next != page.next {
			t.Fatalf("unexpected page: %s", rec.Body.String())
		}
	}
}
