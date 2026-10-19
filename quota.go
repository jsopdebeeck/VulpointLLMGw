package main

import (
	"fmt"
	"sync"
	"time"
)

// Meter keeps live usage counters per user ("u:name") and per target ("t:name")
// for the current minute, day and month. It is preloaded from the database at startup.
type Meter struct {
	mu         sync.Mutex
	minute     int64
	day, month string
	rpm        map[string]int64
	dReq, dTok map[string]int64
	mReq, mTok map[string]int64
}

func NewMeter() *Meter { m := &Meter{}; m.roll(time.Now()); return m }

func (m *Meter) roll(now time.Time) {
	if min := now.Unix() / 60; min != m.minute {
		m.minute, m.rpm = min, map[string]int64{}
	}
	if d := now.Format("2006-01-02"); d != m.day {
		m.day, m.dReq, m.dTok = d, map[string]int64{}, map[string]int64{}
	}
	if mo := now.Format("2006-01"); mo != m.month {
		m.month, m.mReq, m.mTok = mo, map[string]int64{}, map[string]int64{}
	}
}

// Exceeded returns the first quota limit already reached for key, or "".
func (m *Meter) Exceeded(key string, q *Quota) string {
	if q == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roll(time.Now())
	return m.exceeded(key, q)
}

// Reserve checks the quota and counts the request in the per-minute rate in one step, so that
// parallel requests cannot all pass a limit they reach together. A request over a "warn"
// quota is counted too. The day and month counters are updated by Count, once admitted.
func (m *Meter) Reserve(key string, q *Quota) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roll(time.Now())
	lim := ""
	if q != nil {
		lim = m.exceeded(key, q)
	}
	if lim == "" || q.Action == "warn" {
		m.rpm[key]++
	}
	return lim
}

// Count records an admitted request whose rate was already counted by Reserve.
func (m *Meter) Count(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roll(time.Now())
	m.dReq[key]++
	m.mReq[key]++
}

func (m *Meter) exceeded(key string, q *Quota) string {
	switch {
	case q.RPM > 0 && m.rpm[key] >= q.RPM:
		return "RPM"
	case q.RPD > 0 && m.dReq[key] >= q.RPD:
		return "RPD"
	case q.TokensDay > 0 && m.dTok[key] >= q.TokensDay:
		return "TOKENS_DAY"
	case q.TokensMonth > 0 && m.mTok[key] >= q.TokensMonth:
		return "TOKENS_MONTH"
	}
	return ""
}

func (m *Meter) Admit(keys ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roll(time.Now())
	for _, k := range keys {
		m.rpm[k]++
		m.dReq[k]++
		m.mReq[k]++
	}
}

func (m *Meter) AddTokens(tok int64, keys ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roll(time.Now())
	for _, k := range keys {
		m.dTok[k] += tok
		m.mTok[k] += tok
	}
}

type Usage struct{ RPM, DayReq, DayTok, MonthReq, MonthTok int64 }

func (m *Meter) Snapshot() map[string]Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roll(time.Now())
	out := map[string]Usage{}
	for _, mp := range []map[string]int64{m.rpm, m.dReq, m.mReq, m.mTok} {
		for k := range mp {
			out[k] = Usage{m.rpm[k], m.dReq[k], m.dTok[k], m.mReq[k], m.mTok[k]}
		}
	}
	return out
}

// Preload rebuilds today's and this month's counters from stored metrics.
func (m *Meter) Preload(s *Store) error {
	now := time.Now()
	day := dayStart(now)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	rs, err := s.db.Query(`SELECT user, target, SUM(d), SUM(dt), SUM(n), SUM(t) FROM (
		SELECT user, target, ts >= ? d, CASE WHEN ts >= ? THEN tok_in + tok_out ELSE 0 END dt, 1 n, tok_in + tok_out t
		FROM requests WHERE ts >= ? AND blocked = 0 AND status > 0
		UNION ALL SELECT user, target, 0, 0, requests, tok_in + tok_out FROM daily WHERE day >= ?
	) GROUP BY 1, 2`, day.Unix(), day.Unix(), month.Unix(), month.Format("2006-01-02"))
	if err != nil {
		return err
	}
	defer rs.Close()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roll(now)
	for rs.Next() {
		var u, t string
		var d, dt, n, tk int64
		if err := rs.Scan(&u, &t, &d, &dt, &n, &tk); err != nil {
			return err
		}
		for _, k := range []string{"u:" + u, "t:" + t} {
			m.dReq[k] += d
			m.dTok[k] += dt
			m.mReq[k] += n
			m.mTok[k] += tk
		}
	}
	return rs.Err()
}

func quotaMsg(scope, name, limit string) string {
	labels := map[string]string{"RPM": "requêtes par minute", "RPD": "requêtes par jour",
		"TOKENS_DAY": "tokens par jour", "TOKENS_MONTH": "tokens par mois"}
	return fmt.Sprintf("quota %s atteint pour %s %q", labels[limit], scope, name)
}
