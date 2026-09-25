package main

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "modernc.org/sqlite"
)

// Rec is one proxied request. Sensitive values are never stored, only counts per kind.
type Rec struct {
	TS                                time.Time
	User, Profile, Target, Model, API string
	Err                               string
	Status                            int
	Stream                            bool
	BytesIn, BytesOut, TokIn, TokOut  int64
	InMs, GwMs, UpMs, TtfbMs, TotalMs int64
	Anon, Blocked                     int
	Dets                              map[string]map[string]int
}

type Store struct {
	db   *sql.DB
	ch   chan *Rec
	done chan struct{}
}

const schema = `
CREATE TABLE IF NOT EXISTS requests(id INTEGER PRIMARY KEY, ts INTEGER, user TEXT, profile TEXT, target TEXT, model TEXT,
  api TEXT, status INTEGER, stream INTEGER, bytes_in INTEGER, bytes_out INTEGER, tok_in INTEGER, tok_out INTEGER,
  in_ms INTEGER, gw_ms INTEGER, up_ms INTEGER, ttfb_ms INTEGER, total_ms INTEGER, anon INTEGER, blocked INTEGER, err TEXT);
CREATE INDEX IF NOT EXISTS requests_ts ON requests(ts);
CREATE TABLE IF NOT EXISTS detections(ts INTEGER, user TEXT, target TEXT, kind TEXT, action TEXT, n INTEGER);
CREATE INDEX IF NOT EXISTS detections_ts ON detections(ts);
CREATE TABLE IF NOT EXISTS daily(day TEXT, user TEXT, target TEXT, requests INTEGER, errors INTEGER, bytes_in INTEGER,
  bytes_out INTEGER, tok_in INTEGER, tok_out INTEGER, sum_in INTEGER, sum_gw INTEGER, sum_up INTEGER, sum_total INTEGER,
  anon INTEGER, blocked INTEGER, PRIMARY KEY(day, user, target));
CREATE TABLE IF NOT EXISTS daily_det(day TEXT, user TEXT, target TEXT, kind TEXT, action TEXT, n INTEGER,
  PRIMARY KEY(day, user, target, kind, action));`

// errExpr: technical errors only (policy blocks are counted as violations).
const errExpr = `((status>=400 OR status=0) AND blocked=0)`

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	s := &Store{db: db, ch: make(chan *Rec, 10000), done: make(chan struct{})}
	if err := s.initDebug(); err != nil {
		return nil, err
	}
	go s.writer()
	return s, nil
}

func (s *Store) Add(r *Rec) {
	select {
	case s.ch <- r:
	default:
		log.Print("métriques : file saturée, enregistrement perdu")
	}
}

func (s *Store) Close() {
	close(s.ch)
	<-s.done
	s.db.Close()
}

func (s *Store) writer() {
	defer close(s.done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var batch []*Rec
	for {
		select {
		case r, ok := <-s.ch:
			if !ok {
				s.flush(batch)
				return
			}
			if batch = append(batch, r); len(batch) >= 200 {
				s.flush(batch)
				batch = nil
			}
		case <-t.C:
			if len(batch) > 0 {
				s.flush(batch)
				batch = nil
			}
		}
	}
}

func (s *Store) flush(batch []*Rec) {
	if len(batch) == 0 {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		log.Print("métriques : ", err)
		return
	}
	defer tx.Rollback()
	for _, r := range batch {
		ts := r.TS.Unix()
		if _, err := tx.Exec(`INSERT INTO requests(ts,user,profile,target,model,api,status,stream,bytes_in,bytes_out,tok_in,tok_out,
			in_ms,gw_ms,up_ms,ttfb_ms,total_ms,anon,blocked,err) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			ts, r.User, r.Profile, r.Target, r.Model, r.API, r.Status, r.Stream, r.BytesIn, r.BytesOut, r.TokIn, r.TokOut,
			r.InMs, r.GwMs, r.UpMs, r.TtfbMs, r.TotalMs, r.Anon, r.Blocked, r.Err); err != nil {
			log.Print("métriques : ", err)
			return
		}
		for kind, acts := range r.Dets {
			for act, n := range acts {
				tx.Exec(`INSERT INTO detections VALUES(?,?,?,?,?,?)`, ts, r.User, r.Target, kind, act, n)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		log.Print("métriques : ", err)
	}
}

// Rollup generalizes raw rows older than rawDays into daily aggregates
// (per day × user × target: per-request detail, model and percentiles are lost).
func (s *Store) Rollup(rawDays, aggDays int) error {
	cut := time.Now().AddDate(0, 0, -rawDays).Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO daily SELECT date(ts,'unixepoch','localtime'), user, target, COUNT(*), SUM(` + errExpr + `),
			SUM(bytes_in), SUM(bytes_out), SUM(tok_in), SUM(tok_out), SUM(in_ms), SUM(gw_ms), SUM(up_ms), SUM(total_ms),
			SUM(anon), SUM(blocked) FROM requests WHERE ts < ? GROUP BY 1,2,3
			ON CONFLICT(day,user,target) DO UPDATE SET requests=requests+excluded.requests, errors=errors+excluded.errors,
			bytes_in=bytes_in+excluded.bytes_in, bytes_out=bytes_out+excluded.bytes_out, tok_in=tok_in+excluded.tok_in,
			tok_out=tok_out+excluded.tok_out, sum_in=sum_in+excluded.sum_in, sum_gw=sum_gw+excluded.sum_gw,
			sum_up=sum_up+excluded.sum_up, sum_total=sum_total+excluded.sum_total, anon=anon+excluded.anon,
			blocked=blocked+excluded.blocked`, []any{cut}},
		{`DELETE FROM requests WHERE ts < ?`, []any{cut}},
		{`INSERT INTO daily_det SELECT date(ts,'unixepoch','localtime'), user, target, kind, action, SUM(n)
			FROM detections WHERE ts < ? GROUP BY 1,2,3,4,5
			ON CONFLICT(day,user,target,kind,action) DO UPDATE SET n=n+excluded.n`, []any{cut}},
		{`DELETE FROM detections WHERE ts < ?`, []any{cut}},
		{`DELETE FROM daily WHERE day < date('now','localtime',?)`, []any{fmt.Sprintf("-%d days", aggDays)}},
		{`DELETE FROM daily_det WHERE day < date('now','localtime',?)`, []any{fmt.Sprintf("-%d days", aggDays)}},
	}
	for _, st := range stmts {
		if _, err := tx.Exec(st.q, st.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---------- statistics ----------

type rangeDef struct {
	sqlFmt string
	start  func(time.Time) time.Time
	step   string // hour | day
	raw    bool   // raw rows only (hour granularity is not kept in aggregates)
}

func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

var ranges = map[string]rangeDef{
	"48h": {"%Y-%m-%d %H:00", func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location()).Add(-47 * time.Hour)
	}, "hour", true},
	"60d": {"%Y-%m-%d", func(t time.Time) time.Time { return dayStart(t).AddDate(0, 0, -59) }, "day", false},
	"6w": {"%Y-W%W", func(t time.Time) time.Time {
		return dayStart(t).AddDate(0, 0, -((int(t.Weekday())+6)%7)-35)
	}, "day", false},
	"12m": {"%Y-%m", func(t time.Time) time.Time { return time.Date(t.Year(), t.Month()-11, 1, 0, 0, 0, 0, t.Location()) }, "day", false},
	"3y":  {"%Y", func(t time.Time) time.Time { return time.Date(t.Year()-2, 1, 1, 0, 0, 0, 0, t.Location()) }, "day", false},
}

// goLabel reproduces SQLite strftime for bucket labels (so empty buckets appear too).
func goLabel(f string, t time.Time) string {
	switch f {
	case "%Y-%m-%d %H:00":
		return t.Format("2006-01-02 15:00")
	case "%Y-%m-%d":
		return t.Format("2006-01-02")
	case "%Y-W%W":
		return fmt.Sprintf("%d-W%02d", t.Year(), (t.YearDay()-1+7-(int(t.Weekday())+6)%7)/7)
	case "%Y-%m":
		return t.Format("2006-01")
	}
	return t.Format("2006")
}

type Row struct {
	B, G                                   string
	N, Err, BI, BO, TI, TO, SI, SG, SU, ST int64
	An, Bl                                 int64
}

func (s *Store) rows(rd rangeDef, grp string, since time.Time) ([]Row, error) {
	daily := 1
	if rd.raw {
		daily = 0
	}
	q := fmt.Sprintf(`SELECT b, g, SUM(n), SUM(er), SUM(bi), SUM(bo), SUM(ti), SUM(tt), SUM(si), SUM(sg), SUM(su), SUM(st), SUM(an), SUM(bl) FROM (
		SELECT strftime('%[1]s', ts, 'unixepoch', 'localtime') b, %[2]s g, 1 n, %[3]s er, bytes_in bi, bytes_out bo, tok_in ti, tok_out tt,
		  in_ms si, gw_ms sg, up_ms su, total_ms st, anon an, blocked bl FROM requests WHERE ts >= ?
		UNION ALL
		SELECT strftime('%[1]s', day) b, %[2]s g, requests, errors, bytes_in, bytes_out, tok_in, tok_out,
		  sum_in, sum_gw, sum_up, sum_total, anon, blocked FROM daily WHERE day >= ? AND ?
	) GROUP BY b, g ORDER BY b, g`, rd.sqlFmt, grp, errExpr)
	rs, err := s.db.Query(q, since.Unix(), since.Format("2006-01-02"), daily)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Row
	for rs.Next() {
		var r Row
		if err := rs.Scan(&r.B, &r.G, &r.N, &r.Err, &r.BI, &r.BO, &r.TI, &r.TO, &r.SI, &r.SG, &r.SU, &r.ST, &r.An, &r.Bl); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func (s *Store) Stats(rng string) (map[string]any, error) {
	rd, ok := ranges[rng]
	if !ok {
		rng, rd = "48h", ranges["48h"]
	}
	now := time.Now()
	since := rd.start(now)

	var buckets []string
	seen := map[string]bool{}
	for t := since; !t.After(now); {
		if l := goLabel(rd.sqlFmt, t); !seen[l] {
			seen[l] = true
			buckets = append(buckets, l)
		}
		if rd.step == "hour" {
			t = t.Add(time.Hour)
		} else {
			t = t.AddDate(0, 0, 1)
		}
	}

	res := map[string]any{"range": rng, "buckets": buckets, "since": since.Unix()}
	for key, grp := range map[string]string{"all": "'all'", "user": "user", "target": "target"} {
		rows, err := s.rows(rd, grp, since)
		if err != nil {
			return nil, err
		}
		res[key] = rows
	}

	daily := 1
	if rd.raw {
		daily = 0
	}
	type det struct {
		User, Target, Kind, Action string
		N                          int64
	}
	var dets []det
	rs, err := s.db.Query(`SELECT user, target, kind, action, SUM(n) FROM (
		SELECT user, target, kind, action, n FROM detections WHERE ts >= ?
		UNION ALL SELECT user, target, kind, action, n FROM daily_det WHERE day >= ? AND ?
	) GROUP BY 1,2,3,4 ORDER BY 5 DESC`, since.Unix(), since.Format("2006-01-02"), daily)
	if err != nil {
		return nil, err
	}
	for rs.Next() {
		var d det
		rs.Scan(&d.User, &d.Target, &d.Kind, &d.Action, &d.N)
		dets = append(dets, d)
	}
	rs.Close()
	res["dets"] = dets

	// percentiles: only computable on raw data (last raw_retention_days)
	pct := map[string]int64{}
	var n int64
	s.db.QueryRow(`SELECT COUNT(*) FROM requests WHERE ts >= ? AND blocked = 0 AND status > 0`, since.Unix()).Scan(&n)
	if n > 0 {
		for _, col := range []string{"total_ms", "up_ms", "gw_ms", "in_ms", "ttfb_ms"} {
			for _, q := range []float64{0.5, 0.95} {
				var v int64
				s.db.QueryRow(fmt.Sprintf(`SELECT %s FROM requests WHERE ts >= ? AND blocked = 0 AND status > 0 ORDER BY %[1]s LIMIT 1 OFFSET ?`, col),
					since.Unix(), int64(q*float64(n-1))).Scan(&v)
				pct[fmt.Sprintf("%s_p%d", col, int(q*100))] = v
			}
		}
	}
	res["pct"] = pct

	type recent struct {
		TS                                 int64
		User, Target, Model, Err           string
		Status                             int
		Stream                             bool
		TokIn, TokOut, TotalMs, UpMs, GwMs int64
		Anon, Blocked                      int
	}
	var rec []recent
	rs, err = s.db.Query(`SELECT ts, user, target, model, err, status, stream, tok_in, tok_out, total_ms, up_ms, gw_ms, anon, blocked
		FROM requests ORDER BY id DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	for rs.Next() {
		var r recent
		rs.Scan(&r.TS, &r.User, &r.Target, &r.Model, &r.Err, &r.Status, &r.Stream, &r.TokIn, &r.TokOut, &r.TotalMs, &r.UpMs, &r.GwMs, &r.Anon, &r.Blocked)
		rec = append(rec, r)
	}
	rs.Close()
	res["recent"] = rec
	return res, nil
}
