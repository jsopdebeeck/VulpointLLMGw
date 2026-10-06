package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Debug archive: full exchanges, in clear, for troubleshooting. Off by default,
// purged after debug.retention_hours. Never enable it on production data longer than needed.

const debugSchema = `CREATE TABLE IF NOT EXISTS debug(id INTEGER PRIMARY KEY, ts INTEGER, user TEXT, profile TEXT, target TEXT,
  model TEXT, api TEXT, status INTEGER, conv INTEGER, blocked INTEGER, err TEXT, req TEXT, resp TEXT, mapping TEXT, events TEXT);
CREATE INDEX IF NOT EXISTS debug_ts ON debug(ts);`

type DebugEntry struct {
	ID                                     int64
	TS                                     int64
	User, Profile, Target, Model, API, Err string
	Status, Conv, Blocked                  int
	Req, Resp                              string            // as sent to / received from the LLM (pseudonymized)
	Mapping                                map[string]string // placeholder id -> original value
	Events                                 []*DebugEvent
}

var (
	reDataURL = regexp.MustCompile(`"(data:[a-zA-Z/+.-]+;base64,)[A-Za-z0-9+/=]{100,}"`)
	reB64     = regexp.MustCompile(`"data":"[A-Za-z0-9+/=]{500,}"`)
)

func truncKB(s string, kb int) string {
	if len(s) > kb<<10 {
		return s[:kb<<10] + "\n…[tronqué]"
	}
	return s
}

// debugBody: the request as sent, without base64 payloads.
func debugBody(b []byte, kb int) string {
	s := reDataURL.ReplaceAllString(string(b), `"$1…"`)
	s = reB64.ReplaceAllString(s, `"data":"[base64 omis]"`)
	return truncKB(s, kb)
}

// responseText extracts the assistant text from a non-streamed response (before restore).
func responseText(v any) string {
	m, _ := v.(map[string]any)
	if m == nil {
		return ""
	}
	var b strings.Builder
	if chs, ok := m["choices"].([]any); ok {
		for _, c := range chs {
			if msg, ok := c.(map[string]any)["message"].(map[string]any); ok {
				if t, ok := msg["content"].(string); ok {
					b.WriteString(t)
				}
			}
		}
	}
	if cs, ok := m["content"].([]any); ok {
		for _, c := range cs {
			if t, ok := c.(map[string]any)["text"].(string); ok {
				b.WriteString(t)
			}
		}
	}
	return b.String()
}

func (s *Store) initDebug() error { _, err := s.db.Exec(debugSchema); return err }

func (s *Store) AddDebug(e *DebugEntry, retentionHours int) {
	mp, _ := json.Marshal(e.Mapping)
	ev, _ := json.Marshal(e.Events)
	if _, err := s.db.Exec(`INSERT INTO debug(ts,user,profile,target,model,api,status,conv,blocked,err,req,resp,mapping,events)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, e.TS, e.User, e.Profile, e.Target, e.Model, e.API, e.Status, e.Conv, e.Blocked,
		e.Err, e.Req, e.Resp, string(mp), string(ev)); err != nil {
		log.Print("debug : ", err)
	}
	s.PurgeDebug(retentionHours)
}

func (s *Store) PurgeDebug(hours int) {
	s.db.Exec(`DELETE FROM debug WHERE ts < ?`, time.Now().Add(-time.Duration(hours)*time.Hour).Unix())
}

func (g *Gateway) handleDebugList(w http.ResponseWriter, r *http.Request) {
	g.store.PurgeDebug(g.C().Debug.RetentionHours)
	rs, err := g.store.db.Query(`SELECT id, ts, user, target, model, status, conv, blocked, err, length(req), events FROM debug ORDER BY id DESC LIMIT 200`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rs.Close()
	type row struct {
		ID, TS                   int64
		User, Target, Model, Err string
		Status, Conv, Blocked    int
		Size                     int
		Kinds                    map[string]int
	}
	out := []row{}
	for rs.Next() {
		var x row
		var ev string
		rs.Scan(&x.ID, &x.TS, &x.User, &x.Target, &x.Model, &x.Status, &x.Conv, &x.Blocked, &x.Err, &x.Size, &ev)
		var es []*DebugEvent
		json.Unmarshal([]byte(ev), &es)
		x.Kinds = map[string]int{}
		for _, e := range es {
			x.Kinds[e.Kind+":"+e.Action] += e.N
		}
		out = append(out, x)
	}
	var n int
	g.store.db.QueryRow(`SELECT COUNT(*) FROM debug`).Scan(&n)
	writeJSON(w, map[string]any{"enabled": g.C().Debug.Enabled, "retention_hours": g.C().Debug.RetentionHours, "total": n, "rows": out})
}

func (g *Gateway) handleDebugGet(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var e DebugEntry
	var mp, ev string
	err := g.store.db.QueryRow(`SELECT id, ts, user, profile, target, model, api, status, conv, blocked, err, req, resp, mapping, events FROM debug WHERE id = ?`, id).
		Scan(&e.ID, &e.TS, &e.User, &e.Profile, &e.Target, &e.Model, &e.API, &e.Status, &e.Conv, &e.Blocked, &e.Err, &e.Req, &e.Resp, &mp, &ev)
	if err != nil {
		http.Error(w, "entrée introuvable (purgée ?)", 404)
		return
	}
	json.Unmarshal([]byte(mp), &e.Mapping)
	json.Unmarshal([]byte(ev), &e.Events)
	g.audit(g.actor(r), "consultation Debug", fmt.Sprintf("échange #%d", e.ID), fmt.Sprintf("%s → %s/%s", e.User, e.Target, e.Model))
	sort.Slice(e.Events, func(i, j int) bool { return e.Events[i].N > e.Events[j].N })
	writeJSON(w, e)
}

func (g *Gateway) handleDebugClear(w http.ResponseWriter, r *http.Request) {
	g.audit(g.actor(r), "archive Debug vidée", "", "")
	g.store.db.Exec(`DELETE FROM debug`)
	g.store.db.Exec(`VACUUM`)
	writeJSON(w, map[string]any{"ok": true})
}

// handleDebugGlobal aggregates every detected value over the whole archive.
func (g *Gateway) handleDebugGlobal(w http.ResponseWriter, r *http.Request) {
	g.store.PurgeDebug(g.C().Debug.RetentionHours)
	rs, err := g.store.db.Query(`SELECT id, ts, user, events FROM debug ORDER BY id`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rs.Close()
	type agg struct {
		Kind, Action, Value, Placeholder string
		N, Exchanges                     int
		Users                            map[string]int
		LastID, LastTS                   int64
	}
	m := map[string]*agg{}
	for rs.Next() {
		var id, ts int64
		var user, ev string
		rs.Scan(&id, &ts, &user, &ev)
		var es []*DebugEvent
		json.Unmarshal([]byte(ev), &es)
		for _, e := range es {
			k := e.Kind + "\x00" + e.Action + "\x00" + e.Value
			a := m[k]
			if a == nil {
				a = &agg{Kind: e.Kind, Action: e.Action, Value: e.Value, Users: map[string]int{}}
				m[k] = a
			}
			a.N += e.N
			a.Exchanges++
			a.Users[user]++
			a.LastID, a.LastTS = id, ts
		}
	}
	out := make([]*agg, 0, len(m))
	for _, a := range m {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Exchanges > out[j].Exchanges || out[i].Exchanges == out[j].Exchanges && out[i].N > out[j].N
	})
	writeJSON(w, out)
}
