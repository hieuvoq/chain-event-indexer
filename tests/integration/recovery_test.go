package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/hieuvoq/chain-event-indexer/internal/httpapi"
	"github.com/hieuvoq/chain-event-indexer/internal/indexer"
	"github.com/hieuvoq/chain-event-indexer/internal/postgres"
	"github.com/hieuvoq/chain-event-indexer/internal/sepolia"
)

func TestRecovery(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL integration test")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := postgres.Open(dsn)
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
	db, err := postgres.Open(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := postgres.Prepare(ctx, db, 100); err != nil {
		t.Fatal(err)
	}

	logs, err := os.ReadFile("testdata/transfers.json")
	if err != nil {
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
				result = json.RawMessage(logs)
			case "0x84":
				result = json.RawMessage("[]")
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
	source := sepolia.NewClient(server.URL)
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
	if _, err := indexer.RunBatch(ctx, db, source, false); err == nil {
		t.Fatal("expected write failure")
	}
	assertState(100, 0)
	if _, err := db.Exec("ALTER TABLE sepolia_usdc_transfers DROP CONSTRAINT test_failure"); err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.RunBatch(ctx, db, source, false); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Prepare(ctx, db, 999); err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.RunBatch(ctx, db, source, true); err != nil {
		t.Fatal(err)
	}
	assertState(132, 2)
	if _, err := indexer.RunBatch(ctx, db, source, false); err != nil {
		t.Fatal(err)
	}
	assertState(164, 2)

	handler := httpapi.NewHandler(db)
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
			Events []sepolia.Transfer
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
