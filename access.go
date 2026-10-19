package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Access requests: a person asks for access from the portal, confirms their address through
// a single-use link sent by email, and is then approved at once (trusted domain) or put on the
// waiting list of the dashboard (refused by default: an undecided request expires).
// The gateway key is never sent by email: it is shown once, on a page opened by a
// single-use link. Links open a page with a button, because mail filters (Safe Links…)
// open the links they find.

type AccessCfg struct {
	Enabled        bool     `toml:"enabled"`
	TrustedDomains []string `toml:"trusted_domains,omitempty"` // confirmed address in these domains: approved at once
	DefaultProfile string   `toml:"default_profile,omitempty"` // profile of approved requests
	Notify         []string `toml:"notify,omitempty"`          // told about requests to decide (default: [alerts] to)
	LinkHours      int      `toml:"link_hours,omitempty,omitzero"`
	ExpireDays     int      `toml:"expire_days,omitempty,omitzero"`
}

func (c *Config) checkAccess() error {
	a := &c.Access
	if a.LinkHours <= 0 {
		a.LinkHours = 24
	}
	if a.ExpireDays <= 0 {
		a.ExpireDays = 14
	}
	for i, d := range a.TrustedDomains {
		a.TrustedDomains[i] = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@"))
	}
	if !a.Enabled {
		return nil
	}
	if a.DefaultProfile == "" || c.Profiles[a.DefaultProfile] == nil {
		return fmt.Errorf("access : default_profile inconnu %q", a.DefaultProfile)
	}
	if c.Alerts.SMTPHost == "" || c.Alerts.From == "" {
		return fmt.Errorf("access : les demandes d'accès envoient des emails : [alerts] smtp_host et from sont requis")
	}
	return nil
}

func (a *AccessCfg) trusted(email string) bool {
	d := email[strings.LastIndexByte(email, '@')+1:]
	for _, t := range a.TrustedDomains {
		if d == t {
			return true
		}
	}
	return false
}

const accessSchema = `CREATE TABLE IF NOT EXISTS access_requests(id INTEGER PRIMARY KEY, ts INTEGER, email TEXT, name TEXT,
  reason TEXT, lang TEXT, ip TEXT, status TEXT, token TEXT, token_exp INTEGER, user TEXT, profile TEXT,
  decided_by TEXT, decided_ts INTEGER);
CREATE INDEX IF NOT EXISTS access_status ON access_requests(status, ts);
CREATE INDEX IF NOT EXISTS access_email ON access_requests(email, ts);`

// statuses: unverified → pending → approved | denied | expired ; unverified → approved (trusted domain)

func hashToken(t string) string { h := sha256.Sum256([]byte(t)); return hex.EncodeToString(h[:]) }

var (
	emailRe = regexp.MustCompile(`^[A-Za-z0-9._%+\-]{1,64}@[A-Za-z0-9\-]+(\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}$`)
	langs   = map[string]bool{"en": true, "fr": true, "nl": true}
)

func clip(s string, n int) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' {
			return ' '
		}
		return r
	}, s))
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// ---------- the request (portal) ----------

func (g *Gateway) handleAccessRequest(w http.ResponseWriter, r *http.Request) {
	cfg := g.C()
	if !cfg.Access.Enabled {
		http.Error(w, "demandes d'accès désactivées", 404)
		return
	}
	var in struct{ Name, Email, Reason, Lang string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&in); err != nil {
		http.Error(w, "requête invalide", 400)
		return
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	name, reason := clip(in.Name, 80), clip(in.Reason, 500)
	if !emailRe.MatchString(email) || name == "" {
		http.Error(w, "nom et adresse email valides requis", 400)
		return
	}
	if !langs[in.Lang] {
		in.Lang = "en"
	}
	ip, now := clientIP(r), time.Now()
	db := g.store.db
	var nIP, nMail, nAll int
	db.QueryRow(`SELECT COUNT(*) FROM access_requests WHERE ip = ? AND ts > ?`, ip, now.Add(-time.Hour).Unix()).Scan(&nIP)
	db.QueryRow(`SELECT COUNT(*) FROM access_requests WHERE email = ? AND ts > ?`, email, now.Add(-24*time.Hour).Unix()).Scan(&nMail)
	db.QueryRow(`SELECT COUNT(*) FROM access_requests WHERE ts > ?`, now.Add(-24*time.Hour).Unix()).Scan(&nAll)
	if nIP >= 5 || nAll >= 300 {
		http.Error(w, "trop de demandes, réessayez plus tard", 429)
		return
	}
	// same answer whatever happens next: the page does not tell which addresses exist
	ok := map[string]any{"ok": true}
	if nMail >= 3 {
		writeJSON(w, ok)
		return
	}
	tok := randToken()
	res, err := db.Exec(`INSERT INTO access_requests(ts, email, name, reason, lang, ip, status, token, token_exp)
		VALUES(?,?,?,?,?,?,'unverified',?,?)`, now.Unix(), email, name, reason, in.Lang, ip, hashToken(tok),
		now.Add(time.Duration(cfg.Access.LinkHours)*time.Hour).Unix())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	id, _ := res.LastInsertId()
	link := cfg.Auth.BaseURL + "/access/verify?t=" + tok
	subj, body := accessMail(in.Lang, "verify", name, link, cfg.Access.LinkHours)
	go func() {
		if err := sendMail(cfg.Alerts, []string{email}, subj, body); err != nil {
			log.Printf("demande d'accès #%d : email de confirmation non envoyé : %v", id, err)
		}
	}()
	writeJSON(w, ok)
}

// ---------- single-use links ----------

// takeToken finds the request carrying this token (still valid) and consumes it.
func (g *Gateway) takeToken(r *http.Request, t string) (*accessReq, error) {
	if len(t) < 20 {
		authFailed(r)
		return nil, sql.ErrNoRows
	}
	a, err := g.store.accessBy(`token = ? AND token_exp >= ?`, hashToken(t), time.Now().Unix())
	if err != nil {
		authFailed(r) // guessing links counts towards a ban
		return nil, err
	}
	g.store.db.Exec(`UPDATE access_requests SET token = '' WHERE id = ?`, a.ID)
	return a, nil
}

func (g *Gateway) handleAccessVerifyPage(w http.ResponseWriter, r *http.Request) {
	accessPage(w, 200, "Confirmer votre adresse", `<p>Confirmez votre adresse email pour transmettre votre demande d'accès.</p>
<form method="post" action="/access/verify"><input type="hidden" name="t" value="`+html.EscapeString(r.URL.Query().Get("t"))+`">
<button type="submit">Confirmer</button></form>`)
}

func (g *Gateway) handleAccessVerify(w http.ResponseWriter, r *http.Request) {
	cfg := g.C()
	a, err := g.takeToken(r, r.PostFormValue("t"))
	if err != nil || a.Status != "unverified" {
		accessPage(w, 400, "Lien invalide ou expiré", `<p>Ce lien a déjà servi ou a expiré. Refaites une demande depuis le portail.</p>`)
		return
	}
	if u := cfg.userByEmail(a.Email); u != "" {
		g.store.setStatus(a.ID, "denied", "", "", "système")
		accessPage(w, 200, "Accès déjà existant", `<p>Un accès existe déjà pour cette adresse. Contactez l'administrateur de la passerelle si vous ne retrouvez pas votre clé.</p>`)
		return
	}
	g.audit(&Actor{Name: a.Email}, "demande d'accès", "Portail", fmt.Sprintf("%s · ip %s", a.Name, a.IP))
	if cfg.Access.Enabled && cfg.Access.trusted(a.Email) {
		user, key, err := g.approve(a, cfg.Access.DefaultProfile, &Actor{Name: "système"}, "accès approuvé automatiquement (domaine de confiance)")
		if err != nil {
			log.Printf("demande d'accès #%d : approbation impossible : %v", a.ID, err)
			g.store.setStatus(a.ID, "pending", "", "", "")
			accessPage(w, 200, "Demande transmise", `<p>Votre adresse est confirmée. Votre demande est transmise à l'administrateur.</p>`)
			return
		}
		accessPage(w, 200, "Accès accordé", keyHTML(user, key))
		return
	}
	g.store.setStatus(a.ID, "pending", "", "", "")
	g.notifyAccess(cfg, a)
	accessPage(w, 200, "Demande transmise", `<p>Votre adresse est confirmée. Votre demande est transmise à l'administrateur, qui l'acceptera ou la refusera. Vous recevrez un email.</p>`)
}

func (g *Gateway) handleAccessKeyPage(w http.ResponseWriter, r *http.Request) {
	accessPage(w, 200, "Votre clé gateway", `<p>Votre clé ne s'affichera qu'une seule fois : préparez-vous à la copier dans un endroit sûr.</p>
<form method="post" action="/access/key"><input type="hidden" name="t" value="`+html.EscapeString(r.URL.Query().Get("t"))+`">
<button type="submit">Afficher ma clé</button></form>`)
}

func (g *Gateway) handleAccessKey(w http.ResponseWriter, r *http.Request) {
	a, err := g.takeToken(r, r.PostFormValue("t"))
	if err != nil || a.Status != "approved" {
		accessPage(w, 400, "Lien invalide ou expiré", `<p>Ce lien a déjà servi ou a expiré. Demandez à l'administrateur de vous renvoyer un lien.</p>`)
		return
	}
	u := g.C().Users[a.User]
	if u == nil || u.Disabled {
		accessPage(w, 403, "Accès indisponible", `<p>Cet accès a été retiré ou désactivé. Contactez l'administrateur.</p>`)
		return
	}
	g.audit(&Actor{Name: a.Email}, "clé récupérée", "Portail", "utilisateur "+a.User+" · ip "+clientIP(r))
	accessPage(w, 200, "Votre clé gateway", keyHTML(a.User, u.Key))
}

func keyHTML(user, key string) string {
	return `<p><span>Votre accès est ouvert. Utilisateur :</span> <b translate="no">` + html.EscapeString(user) + `</b></p>
<p>Voici votre clé gateway personnelle. Elle ne sera plus affichée : copiez-la maintenant.</p>
<p><input id="k" readonly translate="no" value="` + html.EscapeString(key) + `" style="width:100%;font:13px ui-monospace,monospace;padding:8px"></p>
<p><button type="button" onclick="navigator.clipboard.writeText(document.getElementById('k').value);this.textContent='✓'">Copier</button> <a href="/chat">Ouvrir le chat</a></p>
<p class="sub">Ne la partagez pas : elle vous identifie. En cas de perte ou de fuite, demandez à l'administrateur d'en générer une nouvelle.</p>`
}

func accessPage(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html lang="fr"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s · LLMGW</title>
<style>body{font:15px/1.55 -apple-system,system-ui,sans-serif;max-width:560px;margin:10vh auto;padding:0 16px;color:#16201f;background:#f4f3f0}
.c{background:#fcfcfb;border:1px solid #e3e2de;border-radius:14px;padding:22px}h2{margin:0 0 10px}button{font:inherit;padding:8px 16px;border-radius:8px;border:0;background:#2a78d6;color:#fff;cursor:pointer}
.sub{color:#858480;font-size:13px}.b{display:flex;align-items:center;gap:10px;margin-bottom:14px}.b img{width:36px;height:36px;border-radius:50%%}
@media (prefers-color-scheme:dark){body{background:#111110;color:#fff}.c{background:#1a1a19;border-color:#2e2e2c}}</style>
<div class="c"><div class="b" translate="no"><img src="/docs/img/vulpoint-logo.jpg" alt=""><b>LLMGW</b></div><h2>%s</h2>%s
<p style="text-align:right"><select class="langsel" aria-label="Langue"></select></p></div>
<script src="/i18n.js"></script><script>document.querySelectorAll(".langsel").forEach(I18N.select)</script></html>`,
		html.EscapeString(title), html.EscapeString(title), body)
}

// ---------- decision ----------

// approve creates the gateway user of a request, with a new key, and records the decision.
func (g *Gateway) approve(a *accessReq, profile string, actor *Actor, action string) (string, string, error) {
	g.saveMu.Lock()
	defer g.saveMu.Unlock()
	cur := g.C()
	if cur.Profiles[profile] == nil {
		return "", "", configError{fmt.Errorf("profil inconnu %q", profile)}
	}
	if u := cur.userByEmail(a.Email); u != "" {
		return "", "", configError{fmt.Errorf("un utilisateur existe déjà pour %s (%s)", a.Email, u)}
	}
	nc := cloneConfig(cur)
	user := userName(a.Email, nc)
	key := genKey()
	desc := "Demande d'accès du " + time.Unix(a.TS, 0).Format("02/01/2006")
	if a.Name != "" {
		desc = a.Name + " · " + desc
	}
	if a.Reason != "" {
		desc += " : " + clip(a.Reason, 120)
	}
	if nc.Users == nil {
		nc.Users = map[string]*User{}
	}
	nc.Users[user] = &User{Description: desc, Kind: "user", Key: key, Email: a.Email, Profile: profile}
	if _, err := g.saveConfig(cur, nc, actor, action+" : "+a.Email); err != nil {
		return "", "", err
	}
	g.store.setStatus(a.ID, "approved", user, profile, actor.Name)
	return user, key, nil
}

func (c *Config) userByEmail(email string) string {
	for n, u := range c.Users {
		if strings.EqualFold(u.Email, email) {
			return n
		}
	}
	return ""
}

var userNameRe = regexp.MustCompile(`[^a-z0-9._-]+`)

// userName: the local part of the address, made unique ("jean.dupont", "jean.dupont-2").
func userName(email string, c *Config) string {
	base := strings.Trim(userNameRe.ReplaceAllString(strings.ToLower(email[:strings.LastIndexByte(email, '@')]), "-"), "-.")
	if base == "" {
		base = "user"
	}
	base = clip(base, 40)
	n := base
	for i := 2; c.Users[n] != nil; i++ {
		n = base + "-" + strconv.Itoa(i)
	}
	return n
}

func genKey() string {
	b := make([]byte, 20)
	rand.Read(b)
	return "gw-" + hex.EncodeToString(b)
}

// keyLink gives an approved request a single-use link to its key, and emails it.
func (g *Gateway) keyLink(cfg *Config, a *accessReq) {
	tok := randToken()
	g.store.db.Exec(`UPDATE access_requests SET token = ?, token_exp = ? WHERE id = ?`, hashToken(tok),
		time.Now().Add(time.Duration(cfg.Access.LinkHours)*time.Hour).Unix(), a.ID)
	subj, body := accessMail(a.Lang, "approved", a.Name, cfg.Auth.BaseURL+"/access/key?t="+tok, cfg.Access.LinkHours)
	go func() {
		if err := sendMail(cfg.Alerts, []string{a.Email}, subj, body); err != nil {
			log.Printf("demande d'accès #%d : email d'approbation non envoyé : %v", a.ID, err)
		}
	}()
}

func (g *Gateway) notifyAccess(cfg *Config, a *accessReq) {
	to := cfg.Access.Notify
	if len(to) == 0 {
		to = cfg.Alerts.To
	}
	if len(to) == 0 {
		return
	}
	body := fmt.Sprintf("Nouvelle demande d'accès à la passerelle LLMGW.\n\nNom : %s\nEmail : %s (adresse confirmée)\nMotif : %s\n\n"+
		"Pour l'accepter ou la refuser : %s/dashboard (onglet Demandes d'accès).\n"+
		"Sans décision, elle sera refusée automatiquement dans %d jours.\n", a.Name, a.Email, a.Reason, cfg.Auth.BaseURL, cfg.Access.ExpireDays)
	go func() {
		if err := sendMail(cfg.Alerts, to, "[llmgw] Demande d'accès : "+a.Email, body); err != nil {
			log.Printf("demande d'accès #%d : administrateurs non prévenus : %v", a.ID, err)
		}
	}()
}

// ---------- administration API ----------

type accessReq struct {
	ID                                    int64
	TS                                    int64
	Email, Name, Reason, Lang, IP, Status string
	User, Profile, DecidedBy              string
	DecidedTS                             int64
	Trusted                               bool
	tokenExp                              int64
}

const accessCols = `id, ts, email, name, reason, lang, ip, status, user, profile, decided_by, decided_ts, token_exp`

func scanAccess(sc interface{ Scan(...any) error }) (*accessReq, error) {
	var a accessReq
	var user, prof, by sql.NullString
	var dts, texp sql.NullInt64
	err := sc.Scan(&a.ID, &a.TS, &a.Email, &a.Name, &a.Reason, &a.Lang, &a.IP, &a.Status, &user, &prof, &by, &dts, &texp)
	a.User, a.Profile, a.DecidedBy, a.DecidedTS, a.tokenExp = user.String, prof.String, by.String, dts.Int64, texp.Int64
	return &a, err
}

func (s *Store) accessBy(where string, args ...any) (*accessReq, error) {
	return scanAccess(s.db.QueryRow(`SELECT `+accessCols+` FROM access_requests WHERE `+where, args...))
}

func (s *Store) setStatus(id int64, status, user, profile, by string) {
	if by == "" {
		s.db.Exec(`UPDATE access_requests SET status = ? WHERE id = ?`, status, id)
		return
	}
	s.db.Exec(`UPDATE access_requests SET status = ?, user = ?, profile = ?, decided_by = ?, decided_ts = ? WHERE id = ?`,
		status, user, profile, by, time.Now().Unix(), id)
}

// ExpireAccess refuses the requests nobody decided in time, and voids the stale links.
func (s *Store) ExpireAccess(expireDays int) {
	now := time.Now()
	s.db.Exec(`UPDATE access_requests SET status = 'expired', token = '', decided_by = 'système', decided_ts = ?
		WHERE (status = 'unverified' AND token_exp < ?) OR (status = 'pending' AND ts < ?)`,
		now.Unix(), now.Unix(), now.AddDate(0, 0, -expireDays).Unix())
	s.db.Exec(`UPDATE access_requests SET token = '' WHERE token != '' AND token_exp < ?`, now.Unix())
	s.db.Exec(`DELETE FROM access_requests WHERE ts < ? AND status IN ('expired', 'denied')`, now.AddDate(-1, 0, 0).Unix())
}

func (g *Gateway) handleAccessList(w http.ResponseWriter, r *http.Request) {
	cfg := g.C()
	rs, err := g.store.db.Query(`SELECT ` + accessCols + ` FROM access_requests ORDER BY id DESC LIMIT 500`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rs.Close()
	out := []*accessReq{}
	pending := 0
	for rs.Next() {
		a, err := scanAccess(rs)
		if err != nil {
			continue
		}
		a.Trusted = cfg.Access.trusted(a.Email)
		if a.Status == "pending" {
			pending++
		}
		out = append(out, a)
	}
	writeJSON(w, map[string]any{"requests": out, "pending": pending, "enabled": cfg.Access.Enabled,
		"smtp": cfg.Alerts.SMTPHost != "" && cfg.Alerts.From != "", "baseURL": cfg.Auth.BaseURL})
}

func (g *Gateway) handleAccessDecide(w http.ResponseWriter, r *http.Request) {
	cfg := g.C()
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a, err := g.store.accessBy(`id = ?`, id)
	if err != nil {
		http.Error(w, "demande introuvable", 404)
		return
	}
	var in struct{ Profile string }
	json.NewDecoder(r.Body).Decode(&in)
	actor := g.actor(r)
	switch r.PathValue("act") {
	case "approve":
		if a.Status != "pending" {
			http.Error(w, "seule une demande en attente, adresse confirmée, peut être acceptée", 400)
			return
		}
		if in.Profile == "" {
			in.Profile = cfg.Access.DefaultProfile
		}
		user, _, err := g.approve(a, in.Profile, actor, "accès approuvé")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		a.User = user
		g.keyLink(g.C(), a)
		writeJSON(w, map[string]any{"ok": true, "user": user})
	case "deny":
		if a.Status != "pending" && a.Status != "unverified" {
			http.Error(w, "demande déjà traitée", 400)
			return
		}
		g.store.setStatus(a.ID, "denied", "", "", actor.Name)
		g.store.db.Exec(`UPDATE access_requests SET token = '' WHERE id = ?`, a.ID)
		g.audit(actor, "accès refusé", "Demandes d'accès", a.Email)
		if a.Status == "pending" {
			subj, body := accessMail(a.Lang, "denied", a.Name, "", 0)
			go sendMail(cfg.Alerts, []string{a.Email}, subj, body)
		}
		writeJSON(w, map[string]any{"ok": true})
	case "resend": // a new link to the key of an approved request
		if a.Status != "approved" || cfg.Users[a.User] == nil {
			http.Error(w, "demande non approuvée, ou utilisateur supprimé", 400)
			return
		}
		g.keyLink(cfg, a)
		g.audit(actor, "lien de clé renvoyé", "Demandes d'accès", a.Email)
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "action inconnue", 404)
	}
}

// ---------- emails (in the language of the request) ----------

func accessMail(lang, kind, name, link string, hours int) (string, string) {
	t := map[string]map[string][2]string{
		"verify": {
			"fr": {"Confirmez votre demande d'accès", "Bonjour %s,\n\nPour confirmer votre demande d'accès à la passerelle IA, ouvrez ce lien puis cliquez sur « Confirmer » :\n\n%s\n\nLe lien est valable %d heures et ne sert qu'une fois. Si vous n'avez rien demandé, ignorez ce message.\n"},
			"en": {"Confirm your access request", "Hello %s,\n\nTo confirm your request for access to the AI gateway, open this link and click « Confirm »:\n\n%s\n\nThe link is valid for %d hours and works only once. If you did not ask for anything, ignore this message.\n"},
			"nl": {"Bevestig uw toegangsaanvraag", "Hallo %s,\n\nOm uw aanvraag voor toegang tot de AI-gateway te bevestigen, opent u deze link en klikt u op « Bevestigen »:\n\n%s\n\nDe link is %d uur geldig en werkt maar één keer. Hebt u niets aangevraagd, negeer dan dit bericht.\n"},
		},
		"approved": {
			"fr": {"Votre accès est accordé", "Bonjour %s,\n\nVotre demande d'accès à la passerelle IA est acceptée. Ouvrez ce lien pour afficher votre clé personnelle (une seule fois) :\n\n%s\n\nLe lien est valable %d heures et ne sert qu'une fois.\n"},
			"en": {"Your access is granted", "Hello %s,\n\nYour request for access to the AI gateway is accepted. Open this link to display your personal key (once only):\n\n%s\n\nThe link is valid for %d hours and works only once.\n"},
			"nl": {"Uw toegang is verleend", "Hallo %s,\n\nUw aanvraag voor toegang tot de AI-gateway is aanvaard. Open deze link om uw persoonlijke sleutel te tonen (slechts één keer):\n\n%s\n\nDe link is %d uur geldig en werkt maar één keer.\n"},
		},
		"denied": {
			"fr": {"Votre demande d'accès", "Bonjour %s,\n\nVotre demande d'accès à la passerelle IA n'a pas été acceptée. Pour plus d'informations, contactez l'administrateur.\n%s"},
			"en": {"Your access request", "Hello %s,\n\nYour request for access to the AI gateway was not accepted. For more information, contact the administrator.\n%s"},
			"nl": {"Uw toegangsaanvraag", "Hallo %s,\n\nUw aanvraag voor toegang tot de AI-gateway werd niet aanvaard. Neem voor meer informatie contact op met de beheerder.\n%s"},
		},
	}
	m := t[kind][lang]
	if m[0] == "" {
		m = t[kind]["en"]
	}
	if kind == "denied" {
		return "[LLMGW] " + m[0], fmt.Sprintf(m[1], name, "")
	}
	return "[LLMGW] " + m[0], fmt.Sprintf(m[1], name, link, hours)
}
