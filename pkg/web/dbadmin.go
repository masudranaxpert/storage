package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// handleDBAdmin serves the Database Studio API used by the /database page:
//
//	GET    /api/dbadmin/tables              -> tables + store stats
//	GET    /api/dbadmin/rows?table=&page=   -> paginated rows (search, limit)
//	GET    /api/dbadmin/entry?table=&key=   -> single entry (pretty JSON)
//	POST   /api/dbadmin/entry               -> create  {table,key,value}
//	PUT    /api/dbadmin/entry               -> update  {table,key,value}
//	DELETE /api/dbadmin/entry?table=&key=   -> delete one row
//	POST   /api/dbadmin/truncate            -> drop all rows {table}
func (s *Server) handleDBAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "store not initialised")
		return
	}

	action := strings.TrimPrefix(r.URL.Path, "/api/dbadmin/")

	// All database studio endpoints require full administrator authorization.
	if !s.requireAdmin(w, r) {
		return
	}

	switch {
	case action == "tables" && r.Method == http.MethodGet:
		tables, err := s.store.AdminTables()
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		stats, _ := s.store.AdminStats()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tables": tables,
			"stats":  stats,
		})

	case action == "rows" && r.Method == http.MethodGet:
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		table := q.Get("table")
		res, err := s.store.AdminListRows(table, q.Get("search"), page, limit)
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if table == "nodes" && res != nil {
			for i := range res.Rows {
				valStr := string(res.Rows[i].Value)
				if strings.Contains(valStr, `"ssh_password"`) {
					var rawMap map[string]interface{}
					if err := json.Unmarshal(res.Rows[i].Value, &rawMap); err == nil {
						if _, ok := rawMap["ssh_password"]; ok {
							rawMap["ssh_password"] = "******"
							if b, err := json.MarshalIndent(rawMap, "", "  "); err == nil {
								res.Rows[i].Value = b
							}
						}
					}
				}
			}
		}
		json.NewEncoder(w).Encode(res)

	case action == "entry" && r.Method == http.MethodGet:
		q := r.URL.Query()
		row, err := s.store.AdminGetEntry(q.Get("table"), q.Get("key"))
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, err.Error())
			return
		}
		json.NewEncoder(w).Encode(row)

	case action == "entry" && (r.Method == http.MethodPost || r.Method == http.MethodPut):
		var req struct {
			Table string          `json:"table"`
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid json body")
			return
		}
		create := r.Method == http.MethodPost
		rawVal := req.Value
		var strVal string
		if err := json.Unmarshal(req.Value, &strVal); err == nil && json.Valid([]byte(strVal)) {
			rawVal = json.RawMessage(strVal)
		}
		if err := s.store.AdminSetEntry(req.Table, req.Key, rawVal, create); err != nil {
			code := http.StatusBadRequest
			if strings.Contains(err.Error(), "already exists") {
				code = http.StatusConflict
			}
			writeJSONErr(w, code, err.Error())
			return
		}
		row, _ := s.store.AdminGetEntry(req.Table, req.Key)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "saved",
			"table":  req.Table,
			"key":    req.Key,
			"row":    row,
		})

	case action == "entry" && r.Method == http.MethodDelete:
		q := r.URL.Query()
		if err := s.store.AdminDeleteEntry(q.Get("table"), q.Get("key")); err != nil {
			writeJSONErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "key": q.Get("key")})

	case action == "batch-delete" && r.Method == http.MethodPost:
		var req struct {
			Table string   `json:"table"`
			Keys  []string `json:"keys"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid json body")
			return
		}
		count := 0
		for _, k := range req.Keys {
			if err := s.store.AdminDeleteEntry(req.Table, k); err == nil {
				count++
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "deleted", "deleted": count})

	case action == "truncate" && r.Method == http.MethodPost:
		var req struct {
			Table string `json:"table"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid json body")
			return
		}
		if req.Table == "*" || req.Table == "all" {
			tables, err := s.store.AdminTables()
			if err != nil {
				writeJSONErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			totalRemoved := 0
			for _, t := range tables {
				rem, _ := s.store.AdminTruncate(t.Name)
				totalRemoved += rem
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "truncated", "removed": totalRemoved, "table": "all"})
			return
		}
		removed, err := s.store.AdminTruncate(req.Table)
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "truncated", "removed": removed, "table": req.Table})

	default:
		writeJSONErr(w, http.StatusNotFound, "unknown dbadmin action")
	}
}

func writeJSONErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
