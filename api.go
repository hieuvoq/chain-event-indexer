package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
)

func eventsAPI(db *sql.DB) http.Handler {
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
		rows, err := db.QueryContext(r.Context(), `SELECT block_number,log_index,tx_hash,sender,recipient,amount_raw::text
			FROM sepolia_usdc_transfers WHERE (block_number,log_index)>($1,$2)
			ORDER BY block_number,log_index LIMIT $3`, block, index, limit+1)
		if err != nil {
			apiError(w, err)
			return
		}
		defer rows.Close()
		page := struct {
			Events []transfer `json:"events"`
			Next   *string    `json:"next_cursor"`
		}{Events: make([]transfer, 0, limit)}
		for rows.Next() {
			var e transfer
			if err := rows.Scan(&e.Block, &e.Index, &e.Tx, &e.From, &e.To, &e.Amount); err != nil {
				apiError(w, err)
				return
			}
			if len(page.Events) == limit {
				last := page.Events[limit-1]
				cursor := fmt.Sprintf("%d:%d", last.Block, last.Index)
				page.Next = &cursor
				break
			}
			page.Events = append(page.Events, e)
		}
		if err := rows.Err(); err != nil {
			apiError(w, err)
			return
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
