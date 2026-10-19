package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Administration accounts (Admin / Support), role checks and the audit log.

// AdminAccount: a person allowed into the dashboard, identified by the email of their SSO login.
type AdminAccount struct {
	Email    string `toml:"email"`
	Name     string `toml:"name,omitempty"`
	Role     string `toml:"role"`            // admin | support
	Debug    bool   `toml:"debug,omitempty"` // support: access to the Debug archive (data in clear)
	Disabled bool   `toml:"disabled,omitempty"`
}

func (c *Config) checkAdmins() error {
	seen := map[string]bool{}
	for _, a := range c.Admins {
		a.Email = strings.ToLower(strings.TrimSpace(a.Email))
		if !strings.Contains(a.Email, "@") {
			return fmt.Errorf("compte d'administration : email invalide %q", a.Email)
		}
		if seen[a.Email] {
			return fmt.Errorf("compte d'administration en double : %s", a.Email)
		}
		seen[a.Email] = true
		if a.Role != "admin" && a.Role != "support" {
			return fmt.Errorf("compte %s : role doit valoir admin ou support", a.Email)
		}
	}
	return nil
}

func (c *Config) adminAccount(email string) *AdminAccount {
	for _, a := range c.Admins {
		if strings.EqualFold(a.Email, email) {
			return a
		}
	}
	return nil
}

// Actor: who is calling the administration API, with which rights.
type Actor struct {
	Name  string // email, "jeton d'administration" or "accès ouvert"
	Role  string // admin | support
	Debug bool   // may read the Debug archive
}

func (a *Actor) admin() bool { return a != nil && a.Role == "admin" }

// actor identifies the caller: admin token, SSO session (role re-read from the current
// account list, so a revocation applies at once), or open mode (no token, no SSO).
func (g *Gateway) actor(r *http.Request) *Actor {
	cfg := g.C()
	tok := cfg.Server.AdminToken
	if tok == "" && len(cfg.Auth.Providers) == 0 && len(cfg.Admins) == 0 {
		return &Actor{Name: "accès ouvert", Role: "admin", Debug: true}
	}
	if tok != "" && subtleEqual(r.Header.Get("X-Admin-Token"), tok) {
		return &Actor{Name: "jeton d'administration", Role: "admin", Debug: true}
	}
	s := g.session(r)
	if s == nil || r.Header.Get(sessionHeader) == "" {
		return nil
	}
	if a := cfg.adminAccount(s.Email); a != nil {
		if a.Disabled {
			return nil
		}
		return &Actor{Name: s.Email, Role: a.Role, Debug: a.Role == "admin" || a.Debug}
	}
	// provider rules, re-evaluated with the current configuration
	switch gr := g.access(cfg, s); {
	case gr == nil:
	case gr.Role == "admin":
		return &Actor{Name: s.Email, Role: "admin", Debug: true}
	case gr.Role == "support":
		return &Actor{Name: s.Email, Role: "support"}
	}
	return nil
}

func subtleEqual(a, b string) bool { return len(a) == len(b) && !subtleNotEqual(a, b) }

// guard wraps an administration endpoint with a rights check.
func (g *Gateway) guard(allow func(*Actor) bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := g.actor(r)
		if a == nil {
			if r.Header.Get("X-Admin-Token") != "" {
				authFailed(r)
				g.auditOnce("fail:adm:"+clientIP(r), 5*time.Minute, &Actor{Name: "inconnu"}, "connexion refusée", "Admin (jeton)", "jeton invalide · ip "+clientIP(r))
			}
			http.Error(w, "connexion administrateur requise", 401)
			return
		}
		if !allow(a) {
			http.Error(w, "accès refusé pour le rôle "+a.Role, 403)
			return
		}
		h(w, r)
	}
}

var (
	roleAdmin = func(a *Actor) bool { return a.Role == "admin" }
	roleStaff = func(a *Actor) bool { return a.Role == "admin" || a.Role == "support" }
	roleDebug = func(a *Actor) bool { return a.Debug }
)

func (g *Gateway) handleWhoami(w http.ResponseWriter, r *http.Request) {
	a := g.actor(r)
	if a.Name == "jeton d'administration" { // SSO sign-ins are recorded by the callback
		g.auditOnce("adm:"+clientIP(r), 30*time.Minute, a, "connexion", "Admin (jeton)", "ip "+clientIP(r))
	}
	writeJSON(w, map[string]any{"name": a.Name, "role": a.Role, "debug": a.Debug})
}

// ---------- sign-ins ----------
// Keys and the admin token have no sign-in step on the server: a sign-in is recorded when
// the caller shows up (chat window, dashboard) or comes back after a pause (applications),
// and refused attempts at most once per address and per 5 minutes.

var loginSeen sync.Map // throttle key → unix time of the last record

func (g *Gateway) auditOnce(key string, gap time.Duration, a *Actor, action, target, detail string) {
	now := time.Now().Unix()
	if v, ok := loginSeen.Load(key); ok && now-v.(int64) < int64(gap/time.Second) {
		return
	}
	loginSeen.Store(key, now)
	g.audit(a, action, target, detail)
}

// noteSignIn records the use of a gateway key: the chat window (/v1/me, every 30 min at most)
// or an application (Hermes, Claude Code…: first request after 8 hours of silence).
// Calls made by the chat page itself are covered by /v1/me.
func (g *Gateway) noteSignIn(r *http.Request, name string, u *User) {
	chat := r.URL.Path == "/v1/me"
	if !chat && (r.Header.Get("Sec-Fetch-Site") != "" || r.Header.Get("Origin") != "") {
		return
	}
	ch, gap := "Application (clé)", 8*time.Hour
	if chat {
		ch, gap = "Chat (clé)", 30*time.Minute
	}
	ip := clientIP(r)
	switch {
	case u != nil:
		detail := fmt.Sprintf("profil %q · ip %s", u.Profile, ip)
		if ua := r.Header.Get("User-Agent"); !chat && ua != "" {
			detail += " · " + truncate(ua, 60)
		}
		g.auditOnce("ok:"+ch+":"+name, gap, &Actor{Name: name}, "connexion", ch, detail)
	case name != "":
		g.auditOnce("off:"+ch+":"+name, 5*time.Minute, &Actor{Name: name}, "connexion refusée", ch, "utilisateur désactivé · ip "+ip)
	default:
		g.auditOnce("fail:"+ch+":"+ip, 5*time.Minute, &Actor{Name: "inconnu"}, "connexion refusée", ch, "clé invalide · ip "+ip)
	}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ---------- audit log ----------

const auditSchema = `CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY, ts INTEGER, actor TEXT, role TEXT,
  action TEXT, target TEXT, detail TEXT);
CREATE INDEX IF NOT EXISTS audit_ts ON audit(ts);`

func (s *Store) initAudit() error { _, err := s.db.Exec(auditSchema); return err }

// audit records an administration event. Never pass sensitive values (keys, data in clear).
func (g *Gateway) audit(a *Actor, action, target, detail string) {
	name, role := "système", ""
	if a != nil {
		name, role = a.Name, a.Role
	}
	if _, err := g.store.db.Exec(`INSERT INTO audit(ts, actor, role, action, target, detail) VALUES(?,?,?,?,?,?)`,
		time.Now().Unix(), name, role, action, target, truncKB(detail, 8)); err != nil {
		log.Print("audit : ", err)
	}
}

func (s *Store) PurgeAudit(days int) {
	s.db.Exec(`DELETE FROM audit WHERE ts < ?`, time.Now().AddDate(0, 0, -days).Unix())
}

func (g *Gateway) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rs, err := g.store.db.Query(`SELECT id, ts, actor, role, action, target, detail FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rs.Close()
	type row struct {
		ID, TS                              int64
		Actor, Role, Action, Target, Detail string
	}
	out := []row{}
	for rs.Next() {
		var x row
		rs.Scan(&x.ID, &x.TS, &x.Actor, &x.Role, &x.Action, &x.Target, &x.Detail)
		out = append(out, x)
	}
	writeJSON(w, out)
}

// configChanges summarises what a configuration save changed, without any secret value.
func configChanges(old, nw *Config) []string {
	var out []string
	same := func(a, b any) bool { x, _ := json.Marshal(a); y, _ := json.Marshal(b); return string(x) == string(y) }
	names := func(m map[string]bool) []string {
		var l []string
		for k := range m {
			l = append(l, k)
		}
		sort.Strings(l)
		return l
	}
	// users: one line per change
	for n, u := range nw.Users {
		o := old.Users[n]
		if o == nil {
			out = append(out, fmt.Sprintf("utilisateur %s créé (profil %s)", n, u.Profile))
			continue
		}
		var ch []string
		if o.Profile != u.Profile {
			ch = append(ch, fmt.Sprintf("profil %s → %s", o.Profile, u.Profile))
		}
		if o.Disabled != u.Disabled {
			ch = append(ch, map[bool]string{true: "bloqué", false: "débloqué"}[u.Disabled])
		}
		if o.Key != u.Key {
			ch = append(ch, "nouvelle clé")
		}
		if o.Email != u.Email {
			ch = append(ch, "email modifié")
		}
		if o.Kind != u.Kind || o.Description != u.Description {
			ch = append(ch, "description ou type modifié")
		}
		if !same(o.Quota, u.Quota) {
			ch = append(ch, "quota modifié")
		}
		if len(ch) > 0 {
			out = append(out, "utilisateur "+n+" : "+strings.Join(ch, ", "))
		}
	}
	for n := range old.Users {
		if nw.Users[n] == nil {
			out = append(out, "utilisateur "+n+" supprimé")
		}
	}
	// other sections: names of what changed
	section := func(label string, oldM, newM map[string]any) {
		changed := map[string]bool{}
		for k, v := range newM {
			if ov, ok := oldM[k]; !ok || !same(ov, v) {
				changed[k] = true
			}
		}
		for k := range oldM {
			if _, ok := newM[k]; !ok {
				changed[k+" (supprimé)"] = true
			}
		}
		if len(changed) > 0 {
			out = append(out, label+" : "+strings.Join(names(changed), ", "))
		}
	}
	toMap := func(v any) map[string]any {
		m := map[string]any{}
		b, _ := json.Marshal(v)
		json.Unmarshal(b, &m)
		return m
	}
	section("profils", toMap(old.Profiles), toMap(nw.Profiles))
	section("LLM cibles", toMap(old.Targets), toMap(nw.Targets))
	for _, x := range []struct {
		label string
		a, b  any
	}{{"détecteurs", old.Detectors, nw.Detectors}, {"liste « jamais anonymiser »", old.Privacy, nw.Privacy},
		{"mode DEBUG", old.Debug, nw.Debug}, {"alertes", old.Alerts, nw.Alerts}, {"SSO", old.Auth, nw.Auth},
		{"comptes d'administration", old.Admins, nw.Admins}, {"serveur", old.Server, nw.Server},
		{"protection web", old.Security, nw.Security}, {"demandes d'accès", old.Access, nw.Access}} {
		if !same(x.a, x.b) {
			out = append(out, x.label+" modifié(s)")
		}
	}
	if len(out) == 0 {
		out = append(out, "aucun changement")
	}
	return out
}
