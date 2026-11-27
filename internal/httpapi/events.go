package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/hieuvoq/chain-event-indexer/internal/postgres"
	"github.com/hieuvoq/chain-event-indexer/internal/sepolia"
)

func NewHandler(db *sql.DB) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 50
		if q.Has("limit") {
			n, err := strconv.Atoi(q.Get("limit"))
			if err != nil || n < 1 || n > 100 {
				http.Error(w, "limit must be between 1 and 100", 400)
				return
			}
			limit = n
		}
		var block, index int64 = -1, -1
		if q.Has("after") {
			b, i, ok := strings.Cut(q.Get("after"), ":")
			var e1, e2 error
			block, e1 = strconv.ParseInt(b, 10, 64)
			index, e2 = strconv.ParseInt(i, 10, 64)
			if !ok || e1 != nil || e2 != nil || block < 0 || index < 0 {
				http.Error(w, "after must be block:log_index", 400)
				return
			}
		}
		events, err := postgres.ListTransfers(r.Context(), db, block, index, limit+1)
		if err != nil {
			apiError(w, err)
			return
		}
		page := struct {
			Events []sepolia.Transfer `json:"events"`
			Next   *string            `json:"next_cursor"`
		}{Events: events}
		if len(events) > limit {
			page.Events = events[:limit]
			last := page.Events[limit-1]
			cursor := fmt.Sprintf("%d:%d", last.Block, last.Index)
			page.Next = &cursor
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(page); err != nil {
			log.Printf("write response: %v", err)
		}
	})
	return mux
}

func apiError(w http.ResponseWriter, err error) {
	log.Printf("query events: %v", err)
	http.Error(w, "events unavailable", http.StatusInternalServerError)
}
