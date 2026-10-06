package main

import (
	"log"
	"net/http"
	"strconv"
	"time"
)

// File log: every file or image whose action includes a log entry (pass_log, block, block_alert,
// blocked images), kept 30 days whatever the Debug mode. Name, extension, size and SHA-256 only:
// the content is never stored.

const fileLogSchema = `CREATE TABLE IF NOT EXISTS file_log(id INTEGER PRIMARY KEY, ts INTEGER, user TEXT, profile TEXT,
  target TEXT, model TEXT, kind TEXT, name TEXT, ext TEXT, size INTEGER, sha TEXT, action TEXT);
CREATE INDEX IF NOT EXISTS file_log_ts ON file_log(ts);
CREATE INDEX IF NOT EXISTS file_log_sha ON file_log(user, sha);`

const fileLogDays = 30

func (s *Store) initFileLog() error { _, err := s.db.Exec(fileLogSchema); return err }

// AddFileLog records the files of a request. Clients resend the whole conversation with each
// call: a file already recorded for the same user with the same action in the last 12 hours
// (same SHA-256, or same name when the content is not inline) is not recorded again.
func (s *Store) AddFileLog(ts time.Time, user, profile, target, model string, evs []FileEvent) {
	since := ts.Add(-12 * time.Hour).Unix()
	for _, e := range evs {
		var n int
		if e.SHA != "" {
			s.db.QueryRow(`SELECT COUNT(*) FROM file_log WHERE user=? AND sha=? AND action=? AND ts>=?`, user, e.SHA, e.Action, since).Scan(&n)
		} else {
			s.db.QueryRow(`SELECT COUNT(*) FROM file_log WHERE user=? AND sha='' AND name=? AND action=? AND ts>=?`, user, e.Name, e.Action, since).Scan(&n)
		}
		if n > 0 {
			continue
		}
		if _, err := s.db.Exec(`INSERT INTO file_log(ts, user, profile, target, model, kind, name, ext, size, sha, action) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			ts.Unix(), user, profile, target, model, e.Kind, e.Name, e.Ext, e.Size, e.SHA, e.Action); err != nil {
			log.Print("journal des fichiers : ", err)
		}
	}
}

func (s *Store) PurgeFileLog() {
	s.db.Exec(`DELETE FROM file_log WHERE ts < ?`, time.Now().AddDate(0, 0, -fileLogDays).Unix())
}

func (g *Gateway) handleFileLog(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rs, err := g.store.db.Query(`SELECT id, ts, user, profile, target, model, kind, name, ext, size, sha, action FROM file_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rs.Close()
	type row struct {
		ID, TS, Size                                               int64
		User, Profile, Target, Model, Kind, Name, Ext, SHA, Action string
	}
	out := []row{}
	for rs.Next() {
		var x row
		rs.Scan(&x.ID, &x.TS, &x.User, &x.Profile, &x.Target, &x.Model, &x.Kind, &x.Name, &x.Ext, &x.Size, &x.SHA, &x.Action)
		out = append(out, x)
	}
	writeJSON(w, out)
}
