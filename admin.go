package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Secrets never leave the server: the dashboard receives keepSecret and sends it back unchanged.
const keepSecret = "__keep__"

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

var builtinInfo = map[string]string{
	"email": "Adresse email", "phone": "Numéro de téléphone (international ou national)",
	"iban": "IBAN (clé de contrôle vérifiée)", "niss": "Numéro de registre national belge (clé vérifiée)",
	"inami": "Numéro INAMI / RIZIV d'un prestataire de soins (1-23456-25-001, clé vérifiée)",
	"card":  "Carte bancaire (Luhn)", "ipv4": "Adresse IPv4",
	"person_title":     "Nom après un titre (Monsieur, Mme, Dr, Dhr… fautes de frappe tolérées) ou un mot de contexte (patient, client, nommé, né…)",
	"person_firstname": "Prénom connu suivi d'un nom (Gérald Keizer) ou NOM + prénom (KEIZER Gérald) ; dictionnaire FR/NL/EN intégré",
	"ref_context":      "Identifiant après un mot de contexte : dossier, patient, n°, réf., contrat, facture, matricule, client, sinistre… (Dossier patient 676888-67)",
	"birthdate":        "Date après « né(e) le », « date de naissance », DOB, geboren… (30/12/2006, 2006-12-30, 30 décembre 2006)",
	"date":             "Toutes les dates (30/12/2006, 2006-12-30, 30 décembre 2006)",
	"address":          "Adresse : rue/avenue/chaussée/straat/laan + numéro, code postal + localité (1000 Bruxelles, B-6041 Gosselies)",
	"place":            "Localité belge (liste intégrée : communes et sections courantes) ou lieu après « domicilié à », « habite à », « né à », « woont in »…",
	"pet":              "Nom d'animal après chien, chat, cheval, hond, kat, dog, cat… (mon chien Rex)",
	"firstname_alone":  "Prénom connu seul (Luc, Els, Tom, Liz…), FR/NL/EN ; sauf prénoms qui sont aussi des mots (Rose, Pierre, May, Will…)",
	"be_health_orgs":   "Hôpitaux et réseaux de soins belges : HUB (aussi H.U.B.), Erasme, Bordet, HUDERF, CHU, UZ, AZ, Chirec, Humani, HELORA…",
	"person_learned":   "Mémoire des noms déjà repérés, fautes de frappe comprises ; seules des empreintes HMAC sont stockées",
}

func cloneConfig(c *Config) *Config {
	var out Config
	b, _ := json.Marshal(c)
	json.Unmarshal(b, &out)
	return &out
}

func (g *Gateway) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cur := g.C()
	c := cloneConfig(cur)
	hints := map[string]string{}
	for n, u := range c.Users {
		if k := cur.Users[n].Key; len(k) > 8 {
			hints[n] = k[:6] + "…" + k[len(k)-2:]
		}
		u.Key = keepSecret
	}
	for _, t := range c.Targets {
		if t.APIKey != "" {
			t.APIKey = keepSecret
		}
		for k := range t.Headers { // custom headers often carry a key
			t.Headers[k] = keepSecret
		}
	}
	keySet := map[string]bool{}
	for n, t := range cur.Targets {
		keySet[n] = t.key != ""
	}
	if c.Server.AdminToken != "" {
		c.Server.AdminToken = keepSecret
	}
	if c.Alerts.SMTPPassword != "" {
		c.Alerts.SMTPPassword = keepSecret
	}
	for _, p := range c.Auth.Providers {
		if p.ClientSecret != "" {
			p.ClientSecret = keepSecret
		}
	}
	writeJSON(w, map[string]any{"config": c, "keyHints": hints, "targetKeySet": keySet, "builtins": builtinInfo, "version": fileVersion(cur.path)})
}

func (g *Gateway) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	g.saveMu.Lock()
	defer g.saveMu.Unlock()
	cur := g.C()
	var in struct {
		Config  *Config
		Version string
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Config == nil {
		http.Error(w, "JSON invalide", 400)
		return
	}
	if v := fileVersion(cur.path); in.Version != "" && in.Version != v { // optimistic locking
		http.Error(w, "config.toml a été modifié depuis l'ouverture du dashboard (à la main ou dans un autre onglet). Rechargez la page, puis refaites vos modifications.", 409)
		return
	}
	nc := in.Config
	fail := func(f string, a ...any) { http.Error(w, fmt.Sprintf(f, a...), 400) }
	actor := g.actor(r)
	if !actor.admin() { // support: only the Users section is taken, whatever else was sent
		base := cloneConfig(cur)
		base.Users = nc.Users
		nc = base
	}

	// A stored secret is kept only for the destination it was entered for: otherwise a stolen
	// admin session could point a target to its own server and collect the provider key.
	if nc.Server.AdminToken == keepSecret {
		nc.Server.AdminToken = cur.Server.AdminToken
	}
	if nc.Alerts.SMTPPassword == keepSecret {
		if !strings.EqualFold(nc.Alerts.SMTPHost, cur.Alerts.SMTPHost) {
			fail("alertes : le serveur SMTP a changé, ressaisissez le mot de passe")
			return
		}
		nc.Alerts.SMTPPassword = cur.Alerts.SMTPPassword
	}
	for _, p := range nc.Auth.Providers {
		if p.ClientSecret == keepSecret {
			old := cur.provider(p.ID)
			if old == nil || old.Type != p.Type || old.TenantID != p.TenantID || old.Issuer != p.Issuer {
				fail("SSO %q : le fournisseur a changé, ressaisissez le code secret", p.ID)
				return
			}
			p.ClientSecret = old.ClientSecret
		}
	}
	for n, t := range nc.Targets {
		if !nameRe.MatchString(n) {
			fail("nom de cible invalide %q (lettres, chiffres, _ . -)", n)
			return
		}
		old := cur.Targets[n]
		same := old != nil && origin(old.BaseURL) == origin(t.BaseURL)
		if t.APIKey == keepSecret {
			if !same {
				fail("cible %q : l'adresse a changé, ressaisissez la clé API", n)
				return
			}
			t.APIKey = old.APIKey
		}
		for k, v := range t.Headers {
			if v == keepSecret {
				if !same || old.Headers[k] == "" {
					fail("cible %q : l'adresse a changé, ressaisissez l'en-tête %s", n, k)
					return
				}
				t.Headers[k] = old.Headers[k]
			}
		}
	}
	if err := checkEnvBindings(cur, nc); err != nil {
		fail("%v", err)
		return
	}
	for n := range nc.Profiles {
		if !nameRe.MatchString(n) {
			fail("nom de profil invalide %q", n)
			return
		}
	}
	for n, u := range nc.Users {
		if !nameRe.MatchString(n) {
			fail("nom d'utilisateur invalide %q", n)
			return
		}
		if u.Key == keepSecret {
			old := cur.Users[n]
			if old == nil {
				fail("utilisateur %q : clé à générer", n)
				return
			}
			u.Key = old.Key
		}
	}

	action := "configuration enregistrée"
	if !actor.admin() {
		action = "utilisateurs enregistrés"
	}
	parsed, err := g.saveConfig(cur, nc, actor, action)
	if err != nil {
		var ce configError
		if errors.As(err, &ce) {
			fail("%v", err)
		} else {
			http.Error(w, err.Error(), 500)
		}
		return
	}
	var notes []string
	if parsed.Server.Listen != cur.Server.Listen || parsed.dbPath != cur.dbPath || parsed.Server.UpstreamTimeout != cur.Server.UpstreamTimeout {
		notes = append(notes, "listen, db et upstream_timeout_sec seront pris en compte au prochain redémarrage")
	}
	writeJSON(w, map[string]any{"ok": true, "notes": notes})
}

// configError: the new configuration is invalid (400), as opposed to a write failure (500).
type configError struct{ error }

// saveConfig validates nc, writes config.toml (previous version kept), applies it at once and
// records the change in the audit log. Callers hold saveMu.
func (g *Gateway) saveConfig(cur, nc *Config, actor *Actor, action string) (*Config, error) {
	var buf bytes.Buffer
	buf.WriteString("# llmgw : configuration gérée par le dashboard (" + time.Now().Format("2006-01-02 15:04") + ").\n" +
		"# Modifiable à la main ; les commentaires ne sont pas conservés. Documentation : config.example.toml\n\n")
	if err := toml.NewEncoder(&buf).Encode(nc); err != nil {
		return nil, configError{fmt.Errorf("encodage : %v", err)}
	}
	parsed, err := ParseConfig(buf.Bytes(), filepath.Dir(cur.path))
	if err != nil {
		return nil, configError{err}
	}
	parsed.path = cur.path
	if err := writeWithBackup(cur.path, buf.Bytes()); err != nil {
		return nil, err
	}
	g.cfg.Store(parsed)
	log.Print("configuration enregistrée : ", action)
	g.audit(actor, action, "", strings.Join(configChanges(cur, parsed), " ; "))
	return parsed, nil
}

// origin: scheme://host of an address, the destination a secret is bound to.
func origin(u string) string {
	p, err := url.Parse(strings.TrimSpace(u))
	if err != nil {
		return strings.ToLower(u)
	}
	return strings.ToLower(p.Scheme + "://" + p.Host)
}

// envRefs: environment variables a configuration sends somewhere, with their destination
// ("*" when several).
func envRefs(c *Config) map[string]string {
	m := map[string]string{}
	add := func(dest string, vals ...string) {
		for _, v := range vals {
			var names []string
			if nameEnvRe.MatchString(v) {
				names = []string{v}
			} else {
				os.Expand(v, func(n string) string { names = append(names, n); return "" })
			}
			for _, n := range names {
				if d, ok := m[n]; ok && d != dest {
					m[n] = "*"
				} else {
					m[n] = dest
				}
			}
		}
	}
	for _, t := range c.Targets {
		add(origin(t.BaseURL), t.APIKeyEnv, t.APIKey)
	}
	for _, p := range c.Auth.Providers {
		add("sso:"+p.Type+":"+p.TenantID+p.Issuer, p.ClientSecretEnv, p.ClientSecret)
	}
	add("smtp:"+strings.ToLower(c.Alerts.SMTPHost), c.Alerts.SMTPPasswordEnv, c.Alerts.SMTPPassword)
	delete(m, "")
	return m
}

var nameEnvRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkEnvBindings: a variable that already feeds one destination cannot be sent to another
// one from the dashboard (that change has to be made in config.toml, on the server).
func checkEnvBindings(cur, nc *Config) error {
	was := envRefs(cur)
	for n, d := range envRefs(nc) {
		if old, ok := was[n]; ok && old != "*" && d != old {
			return fmt.Errorf("la variable %s sert déjà à %s : elle ne peut pas être envoyée ailleurs depuis le dashboard (modifier config.toml sur le serveur)", n, strings.TrimPrefix(old, "sso:"))
		}
	}
	return nil
}

// writeWithBackup keeps the 10 last versions as <file>.bak-YYYYMMDD-HHMMSS; older ones are
// moved to _archive/config/ next to the file, never deleted.
func writeWithBackup(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak-"+time.Now().Format("20060102-150405"), old, 0o600); err != nil {
			return err
		}
		baks, _ := filepath.Glob(path + ".bak-*")
		sort.Strings(baks)
		if len(baks) > 10 {
			arch := filepath.Join(filepath.Dir(path), "_archive", "config")
			if err := os.MkdirAll(arch, 0o700); err == nil {
				for _, b := range baks[:len(baks)-10] {
					os.Rename(b, filepath.Join(arch, filepath.Base(b)))
				}
			}
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (g *Gateway) handleUsage(w http.ResponseWriter, r *http.Request) {
	cfg := g.C()
	uq := map[string]Quota{}
	for n, u := range cfg.Users {
		uq[n] = cfg.EffectiveQuota(u)
	}
	tq := map[string]*Quota{}
	for n, t := range cfg.Targets {
		tq[n] = t.Quota
	}
	writeJSON(w, map[string]any{"usage": g.meter.Snapshot(), "userQuota": uq, "targetQuota": tq})
}

// handleTargetTest lists the models of a saved target (connectivity + key check).
func (g *Gateway) handleTargetTest(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	json.NewDecoder(r.Body).Decode(&in)
	t := g.C().Targets[in.Name]
	if t == nil {
		http.Error(w, "cible inconnue (enregistrer d'abord)", 400)
		return
	}
	url := strings.TrimRight(t.BaseURL, "/") + "/models"
	if t.API == "anthropic" {
		url = strings.TrimRight(t.BaseURL, "/") + "/v1/models"
	}
	req, _ := http.NewRequestWithContext(r.Context(), "GET", url, nil)
	if t.API == "anthropic" {
		req.Header.Set("anthropic-version", "2023-06-01")
		if t.key != "" {
			req.Header.Set("x-api-key", t.key)
		}
	} else if t.key != "" {
		req.Header.Set("Authorization", "Bearer "+t.key)
	}
	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}
	t0 := time.Now()
	cl := &http.Client{Timeout: 10 * time.Second, Transport: g.client.Transport}
	resp, err := cl.Do(req)
	res := map[string]any{"url": url}
	if err != nil {
		res["error"] = err.Error()
		writeJSON(w, res)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	res["status"], res["ms"] = resp.StatusCode, time.Since(t0).Milliseconds()
	var m struct{ Data []struct{ ID string } }
	if json.Unmarshal(body, &m) == nil {
		var ids []string
		for _, d := range m.Data {
			ids = append(ids, d.ID)
		}
		sort.Strings(ids)
		res["models"] = ids
	}
	if resp.StatusCode >= 400 {
		res["error"] = strings.TrimSpace(string(body[:min(len(body), 300)]))
	}
	writeJSON(w, res)
}

// fileVersion identifies the current content of config.toml.
func fileVersion(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:8])
}
