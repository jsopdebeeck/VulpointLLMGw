package main

import (
	"bytes"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

// Single sign-on for the web portal and the dashboard: OpenID Connect (authorization code + PKCE)
// with Microsoft Entra ID, Google or any OIDC provider. Gateway keys and the admin token keep working.

type AuthCfg struct {
	BaseURL      string         `toml:"base_url,omitempty"`               // public address, for redirect URIs
	SessionHours int            `toml:"session_hours,omitempty,omitzero"` // web session lifetime (default 12)
	DisableKeys  bool           `toml:"disable_keys,omitempty"`           // portal: SSO only, no key login
	Providers    []*ProviderCfg `toml:"providers,omitempty"`
}

type ProviderCfg struct {
	ID              string         `toml:"id"`
	Type            string         `toml:"type"` // entra | google | oidc
	Label           string         `toml:"label,omitempty"`
	TenantID        string         `toml:"tenant_id,omitempty"` // entra
	Issuer          string         `toml:"issuer,omitempty"`    // oidc
	ClientID        string         `toml:"client_id"`
	ClientSecret    string         `toml:"client_secret,omitempty"`
	ClientSecretEnv string         `toml:"client_secret_env,omitempty"`
	AllowedDomains  []string       `toml:"allowed_domains,omitempty"` // email domains accepted
	DefaultProfile  string         `toml:"default_profile,omitempty"` // when no mapping matches ("" = refuse)
	AdminEmails     []string       `toml:"admin_emails,omitempty"`
	AdminGroups     []string       `toml:"admin_groups,omitempty"` // Entra group object IDs
	AdminRoles      []string       `toml:"admin_roles,omitempty"`  // Entra app roles
	SupportEmails   []string       `toml:"support_emails,omitempty"`
	SupportGroups   []string       `toml:"support_groups,omitempty"`
	SupportRoles    []string       `toml:"support_roles,omitempty"`
	Mappings        []*AuthMapping `toml:"mappings,omitempty"` // first match wins

	secret string
	issuer string
}

// AuthMapping gives a profile to the identities matching a group, an app role or an email pattern.
type AuthMapping struct {
	Group   string `toml:"group,omitempty"`
	Role    string `toml:"role,omitempty"`
	Email   string `toml:"email,omitempty"` // "jane@x.be" or "*@x.be"
	Profile string `toml:"profile"`
}

func (p *ProviderCfg) display() string {
	if p.Label != "" {
		return p.Label
	}
	switch p.Type {
	case "entra":
		return "Microsoft"
	case "google":
		return "Google"
	}
	return p.ID
}

// checkAuth validates the [auth] section (called from ParseConfig).
func (c *Config) checkAuth() error {
	a := &c.Auth
	if a.SessionHours <= 0 {
		a.SessionHours = 12
	}
	if a.BaseURL == "" {
		a.BaseURL = "http://" + c.Server.Listen
	}
	a.BaseURL = strings.TrimRight(a.BaseURL, "/")
	seen := map[string]bool{}
	for _, p := range a.Providers {
		if !nameRe.MatchString(p.ID) || seen[p.ID] {
			return fmt.Errorf("auth : identifiant de fournisseur invalide ou en double %q", p.ID)
		}
		seen[p.ID] = true
		switch p.Type {
		case "entra":
			if p.TenantID == "" {
				return fmt.Errorf("auth %q : tenant_id requis", p.ID)
			}
			p.issuer = "https://login.microsoftonline.com/" + p.TenantID + "/v2.0"
		case "google":
			p.issuer = "https://accounts.google.com"
		case "oidc":
			if p.Issuer == "" {
				return fmt.Errorf("auth %q : issuer requis", p.ID)
			}
			p.issuer = strings.TrimRight(p.Issuer, "/")
		default:
			return fmt.Errorf("auth %q : type doit valoir entra, google ou oidc", p.ID)
		}
		if p.ClientID == "" {
			return fmt.Errorf("auth %q : client_id requis", p.ID)
		}
		p.secret = p.ClientSecret
		if p.ClientSecretEnv != "" {
			p.secret = os.Getenv(p.ClientSecretEnv)
		}
		if p.DefaultProfile != "" && c.Profiles[p.DefaultProfile] == nil {
			return fmt.Errorf("auth %q : default_profile inconnu %q", p.ID, p.DefaultProfile)
		}
		for _, m := range p.Mappings {
			if c.Profiles[m.Profile] == nil {
				return fmt.Errorf("auth %q : profil inconnu %q dans mappings", p.ID, m.Profile)
			}
			if m.Group == "" && m.Role == "" && m.Email == "" {
				return fmt.Errorf("auth %q : une règle de mappings doit préciser group, role ou email", p.ID)
			}
		}
	}
	return nil
}

func (c *Config) provider(id string) *ProviderCfg {
	for _, p := range c.Auth.Providers {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// ---------- OIDC discovery and ID token verification (stdlib only) ----------

type oidcClient struct {
	mu      sync.Mutex
	issuer  string
	Auth    string `json:"authorization_endpoint"`
	Token   string `json:"token_endpoint"`
	JWKSURI string `json:"jwks_uri"`
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

var oidcClients sync.Map // issuer -> *oidcClient

var httpAuth = &http.Client{Timeout: 15 * time.Second}

func discover(issuer string) (*oidcClient, error) {
	if v, ok := oidcClients.Load(issuer); ok {
		return v.(*oidcClient), nil
	}
	resp, err := httpAuth.Get(issuer + "/.well-known/openid-configuration")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("découverte OIDC : HTTP %d", resp.StatusCode)
	}
	c := &oidcClient{issuer: issuer}
	if err := json.NewDecoder(resp.Body).Decode(c); err != nil || c.Auth == "" || c.Token == "" || c.JWKSURI == "" {
		return nil, fmt.Errorf("découverte OIDC : document incomplet")
	}
	oidcClients.Store(issuer, c)
	return c, nil
}

func (c *oidcClient) key(kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if k := c.keys[kid]; k != nil {
		return k, nil
	}
	if time.Since(c.fetched) < 30*time.Second && c.keys != nil {
		return nil, fmt.Errorf("clé de signature inconnue %q", kid)
	}
	resp, err := httpAuth.Get(c.JWKSURI)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var set struct {
		Keys []struct{ Kid, Kty, N, E string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, err
	}
	c.keys, c.fetched = map[string]*rsa.PublicKey{}, time.Now()
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}
		c.keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if k := c.keys[kid]; k != nil {
		return k, nil
	}
	return nil, fmt.Errorf("clé de signature inconnue %q", kid)
}

// verifyIDToken checks the RS256 signature, issuer, audience, expiry and nonce.
func verifyIDToken(c *oidcClient, p *ProviderCfg, tok, nonce string) (map[string]any, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, errors.New("jeton mal formé")
	}
	var hdr struct{ Alg, Kid string }
	if b, err := base64.RawURLEncoding.DecodeString(parts[0]); err != nil || json.Unmarshal(b, &hdr) != nil {
		return nil, errors.New("en-tête de jeton illisible")
	}
	if hdr.Alg != "RS256" {
		return nil, fmt.Errorf("algorithme non accepté %q", hdr.Alg)
	}
	key, err := c.key(hdr.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("signature illisible")
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, h[:], sig); err != nil {
		return nil, errors.New("signature invalide")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("contenu de jeton illisible")
	}
	v, err := decodeJSON(raw)
	claims, ok := v.(map[string]any)
	if err != nil || !ok {
		return nil, errors.New("contenu de jeton illisible")
	}
	iss, _ := claims["iss"].(string)
	if iss != p.issuer && !(p.Type == "google" && iss == "accounts.google.com") {
		return nil, fmt.Errorf("émetteur inattendu %q", iss)
	}
	audOK := false
	switch a := claims["aud"].(type) {
	case string:
		audOK = a == p.ClientID
	case []any:
		for _, x := range a {
			audOK = audOK || x == p.ClientID
		}
	}
	if !audOK {
		return nil, errors.New("audience inattendue")
	}
	now := time.Now().Unix()
	if exp := num(claims["exp"]); exp == 0 || exp < now-60 {
		return nil, errors.New("jeton expiré")
	}
	if n, _ := claims["nonce"].(string); n != nonce {
		return nil, errors.New("nonce invalide")
	}
	return claims, nil
}

// ---------- signed cookies ----------

type webSession struct {
	User, Email, Name, Provider, Profile string
	Admin                                bool   // kept for sessions opened before roles existed
	Role                                 string // admin | support | ""
	Exp                                  int64
}

type loginState struct {
	State, Nonce, Verifier, Provider, Next string
	Exp                                    int64
}

const (
	sessionCookie = "llmgw_session"
	loginCookie   = "llmgw_login"
	sessionHeader = "X-LLMGW-Session" // required with the cookie: blocks cross-site requests
)

func metaKey(db *sql.DB, name string) ([]byte, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v BLOB)`); err != nil {
		return nil, err
	}
	var k []byte
	if err := db.QueryRow(`SELECT v FROM meta WHERE k = ?`, name).Scan(&k); err == nil && len(k) >= 32 {
		return k, nil
	}
	k = make([]byte, 32)
	rand.Read(k)
	_, err := db.Exec(`INSERT OR REPLACE INTO meta VALUES(?, ?)`, name, k)
	return k, err
}

func (g *Gateway) seal(v any) string {
	b, _ := json.Marshal(v)
	p := base64.RawURLEncoding.EncodeToString(b)
	m := hmac.New(sha256.New, g.sessKey)
	m.Write([]byte(p))
	return p + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (g *Gateway) open(s string, v any) bool {
	i := strings.LastIndexByte(s, '.')
	if i < 0 || len(g.sessKey) == 0 {
		return false
	}
	m := hmac.New(sha256.New, g.sessKey)
	m.Write([]byte(s[:i]))
	sig, err := base64.RawURLEncoding.DecodeString(s[i+1:])
	if err != nil || !hmac.Equal(sig, m.Sum(nil)) {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(s[:i])
	return err == nil && json.Unmarshal(b, v) == nil
}

func (g *Gateway) setCookie(w http.ResponseWriter, name, val, p string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: val, Path: p, MaxAge: maxAge, HttpOnly: true,
		Secure: strings.HasPrefix(g.C().Auth.BaseURL, "https://"), SameSite: http.SameSiteLaxMode})
}

// session returns the valid web session carried by the request, if any.
func (g *Gateway) session(r *http.Request) *webSession {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	var s webSession
	if !g.open(c.Value, &s) || s.Exp < time.Now().Unix() {
		return nil
	}
	return &s
}

// sessionUser maps a web session to a gateway user: the declared user with this email
// (current profile, quotas, disabled flag) or a session-only user with the profile chosen at login.
func (g *Gateway) sessionUser(cfg *Config, s *webSession) (string, *User) {
	if u := cfg.Users[s.User]; u != nil && strings.EqualFold(u.Email, s.Email) {
		if u.Disabled {
			return s.User, nil
		}
		return s.User, u
	}
	if s.Profile == "" || cfg.Profiles[s.Profile] == nil {
		return s.User, nil
	}
	return s.User, &User{Profile: s.Profile, Kind: "user", Email: s.Email, Description: "SSO " + s.Provider}
}

// ---------- identity -> gateway user, profile and admin right ----------

func strList(v any) []string {
	var out []string
	if a, ok := v.([]any); ok {
		for _, x := range a {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

func emailMatches(pattern, email string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if strings.HasPrefix(pattern, "*@") {
		return strings.HasSuffix(email, pattern[1:])
	}
	return pattern == email
}

func (cfg *Config) resolveSSO(p *ProviderCfg, claims map[string]any) (*webSession, error) {
	email := ""
	for _, k := range []string{"email", "preferred_username", "upn"} {
		if v, _ := claims[k].(string); v != "" && strings.Contains(v, "@") {
			email = strings.ToLower(v)
			break
		}
	}
	if email == "" {
		return nil, errors.New("le fournisseur n'a pas transmis d'adresse email")
	}
	if p.Type == "google" {
		if v, ok := claims["email_verified"].(bool); ok && !v {
			return nil, errors.New("adresse email Google non vérifiée")
		}
	}
	if len(p.AllowedDomains) > 0 {
		ok := false
		for _, d := range p.AllowedDomains {
			ok = ok || strings.HasSuffix(email, "@"+strings.ToLower(strings.TrimPrefix(d, "@")))
		}
		if !ok {
			return nil, fmt.Errorf("le domaine de %s n'est pas autorisé", email)
		}
	}
	name, _ := claims["name"].(string)
	groups, roles := strList(claims["groups"]), strList(claims["roles"])
	// dashboard role: the account list first, then the provider's admin / support rules
	matches := func(emails, grps, rls []string) bool {
		ok := contains(emails, email)
		for _, gr := range groups {
			ok = ok || contains(grps, gr)
		}
		for _, ro := range roles {
			ok = ok || contains(rls, ro)
		}
		return ok
	}
	role := ""
	if a := cfg.adminAccount(email); a != nil {
		if !a.Disabled {
			role = a.Role
		}
	} else if matches(p.AdminEmails, p.AdminGroups, p.AdminRoles) {
		role = "admin"
	} else if matches(p.SupportEmails, p.SupportGroups, p.SupportRoles) {
		role = "support"
	}
	admin := role != ""
	s := &webSession{User: email, Email: email, Name: name, Provider: p.display(), Admin: role == "admin", Role: role}
	for n, u := range cfg.Users { // declared user with this email: its own profile and quotas
		if strings.EqualFold(u.Email, email) {
			if u.Disabled {
				return nil, fmt.Errorf("l'accès de %s est désactivé", email)
			}
			s.User, s.Profile = n, u.Profile
			return s, nil
		}
	}
	for _, m := range p.Mappings {
		if (m.Group != "" && contains(groups, m.Group)) || (m.Role != "" && contains(roles, m.Role)) ||
			(m.Email != "" && emailMatches(m.Email, email)) {
			s.Profile = m.Profile
			return s, nil
		}
	}
	s.Profile = p.DefaultProfile
	if s.Profile == "" && !admin {
		return nil, fmt.Errorf("aucun profil n'est attribué à %s (groupes, rôles et domaine sans correspondance)", email)
	}
	return s, nil
}

// ---------- HTTP endpoints ----------

func randToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/chat"
	}
	return path.Clean(next)
}

func (g *Gateway) handleAuthProviders(w http.ResponseWriter, r *http.Request) {
	cfg := g.C()
	type pv struct{ ID, Label, Type string }
	out := []pv{}
	for _, p := range cfg.Auth.Providers {
		out = append(out, pv{p.ID, p.display(), p.Type})
	}
	writeJSON(w, map[string]any{"providers": out, "keys": !cfg.Auth.DisableKeys})
}

func (g *Gateway) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	cfg := g.C()
	p := cfg.provider(r.PathValue("id"))
	if p == nil {
		http.Error(w, "fournisseur inconnu", 404)
		return
	}
	if p.secret == "" {
		src := "client_secret"
		if p.ClientSecretEnv != "" {
			src = "la variable d'environnement " + p.ClientSecretEnv
		}
		authFail(w, 500, "Le code secret du fournisseur "+p.display()+" n'est pas défini ("+src+"). Définissez-le, puis relancez la passerelle.")
		return
	}
	c, err := discover(p.issuer)
	if err != nil {
		authFail(w, 502, "Le fournisseur d'identité est injoignable : "+err.Error())
		return
	}
	st := loginState{State: randToken(), Nonce: randToken(), Verifier: randToken(), Provider: p.ID,
		Next: safeNext(r.URL.Query().Get("next")), Exp: time.Now().Add(10 * time.Minute).Unix()}
	g.setCookie(w, loginCookie, g.seal(st), "/auth/", 600)
	ch := sha256.Sum256([]byte(st.Verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {p.ClientID}, "redirect_uri": {cfg.Auth.BaseURL + "/auth/callback/" + p.ID},
		"scope": {"openid email profile"}, "state": {st.State}, "nonce": {st.Nonce}, "response_mode": {"query"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(ch[:])}, "code_challenge_method": {"S256"}, "prompt": {"select_account"}}
	if p.Type == "google" && len(p.AllowedDomains) == 1 {
		q.Set("hd", p.AllowedDomains[0])
	}
	http.Redirect(w, r, c.Auth+"?"+q.Encode(), http.StatusFound)
}

func (g *Gateway) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	cfg := g.C()
	p := cfg.provider(r.PathValue("id"))
	var st loginState
	ck, err := r.Cookie(loginCookie)
	if p == nil || err != nil || !g.open(ck.Value, &st) || st.Exp < time.Now().Unix() || st.Provider != p.ID {
		authFail(w, 400, "Session de connexion expirée ou invalide. Recommencez la connexion.")
		return
	}
	g.setCookie(w, loginCookie, "", "/auth/", -1)
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		authFail(w, 403, "Connexion refusée par le fournisseur : "+e+" "+q.Get("error_description"))
		return
	}
	if subtleNotEqual(q.Get("state"), st.State) {
		authFail(w, 400, "Paramètre state invalide. Recommencez la connexion.")
		return
	}
	c, err := discover(p.issuer)
	if err != nil {
		authFail(w, 502, "Le fournisseur d'identité est injoignable : "+err.Error())
		return
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {q.Get("code")}, "redirect_uri": {cfg.Auth.BaseURL + "/auth/callback/" + p.ID},
		"client_id": {p.ClientID}, "client_secret": {p.secret}, "code_verifier": {st.Verifier}}
	resp, err := httpAuth.PostForm(c.Token, form)
	if err != nil {
		authFail(w, 502, "Échange du code impossible : "+err.Error())
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
		Desc    string `json:"error_description"`
	}
	json.Unmarshal(body, &tr)
	if resp.StatusCode != 200 || tr.IDToken == "" {
		authFail(w, 502, fmt.Sprintf("Le fournisseur a refusé l'échange du code (HTTP %d) : %s %s", resp.StatusCode, tr.Error, tr.Desc))
		return
	}
	claims, err := verifyIDToken(c, p, tr.IDToken, st.Nonce)
	if err != nil {
		authFail(w, 403, "Jeton d'identité refusé : "+err.Error())
		return
	}
	s, err := cfg.resolveSSO(p, claims)
	if err != nil {
		log.Printf("sso %s : refusé : %v", p.ID, err)
		authFail(w, 403, "Accès refusé : "+err.Error()+". Contactez l'administrateur de la passerelle.")
		return
	}
	s.Exp = time.Now().Add(time.Duration(cfg.Auth.SessionHours) * time.Hour).Unix()
	g.setCookie(w, sessionCookie, g.seal(s), "/", cfg.Auth.SessionHours*3600)
	log.Printf("sso %s : %s connecté (profil %q, rôle %q)", p.ID, s.User, s.Profile, s.Role)
	g.audit(&Actor{Name: s.Email, Role: s.Role}, "connexion", p.display(), fmt.Sprintf("profil %q, rôle %q", s.Profile, s.Role))
	http.Redirect(w, r, st.Next, http.StatusFound)
}

func subtleNotEqual(a, b string) bool { return a == "" || !hmac.Equal([]byte(a), []byte(b)) }

func (g *Gateway) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	s := g.session(r)
	if s == nil {
		http.Error(w, "pas de session", 401)
		return
	}
	writeJSON(w, map[string]any{"user": s.User, "email": s.Email, "name": s.Name, "provider": s.Provider,
		"profile": s.Profile, "admin": s.Admin || s.Role == "admin", "role": s.Role, "expires": s.Exp})
}

func (g *Gateway) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	g.setCookie(w, sessionCookie, "", "/", -1)
	writeJSON(w, map[string]any{"ok": true})
}

func authFail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	var b bytes.Buffer
	b.WriteString(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connexion</title>` +
		`<body style="font:15px/1.5 -apple-system,system-ui,sans-serif;max-width:520px;margin:12vh auto;padding:0 16px;color:#16201f">` +
		`<h2 style="margin:0 0 8px">Connexion impossible</h2><p>`)
	b.WriteString(html.EscapeString(msg))
	b.WriteString(`</p><p><a href="/chat">Retour au portail</a> · <a href="/dashboard">Dashboard</a></p></body>`)
	w.Write(b.Bytes())
}
