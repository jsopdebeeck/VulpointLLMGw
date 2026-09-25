package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
	"card": "Carte bancaire (Luhn)", "ipv4": "Adresse IPv4",
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

	if nc.Server.AdminToken == keepSecret {
		nc.Server.AdminToken = cur.Server.AdminToken
	}
	if nc.Alerts.SMTPPassword == keepSecret {
		nc.Alerts.SMTPPassword = cur.Alerts.SMTPPassword
	}
	for n, t := range nc.Targets {
		if !nameRe.MatchString(n) {
			fail("nom de cible invalide %q (lettres, chiffres, _ . -)", n)
			return
		}
		if t.APIKey == keepSecret {
			if old := cur.Targets[n]; old != nil {
				t.APIKey = old.APIKey
			} else {
				t.APIKey = ""
			}
		}
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

	var buf bytes.Buffer
	buf.WriteString("# llmgw : configuration gérée par le dashboard (" + time.Now().Format("2006-01-02 15:04") + ").\n" +
		"# Modifiable à la main ; les commentaires ne sont pas conservés. Documentation : config.example.toml\n\n")
	if err := toml.NewEncoder(&buf).Encode(nc); err != nil {
		fail("encodage : %v", err)
		return
	}
	parsed, err := ParseConfig(buf.Bytes(), filepath.Dir(cur.path))
	if err != nil {
		fail("%v", err)
		return
	}
	parsed.path = cur.path
	if err := writeWithBackup(cur.path, buf.Bytes()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	g.cfg.Store(parsed)
	log.Print("configuration enregistrée depuis le dashboard")
	var notes []string
	if parsed.Server.Listen != cur.Server.Listen || parsed.dbPath != cur.dbPath || parsed.Server.UpstreamTimeout != cur.Server.UpstreamTimeout {
		notes = append(notes, "listen, db et upstream_timeout_sec seront pris en compte au prochain redémarrage")
	}
	writeJSON(w, map[string]any{"ok": true, "notes": notes})
}

// writeWithBackup keeps the 10 last versions as <file>.bak-YYYYMMDD-HHMMSS.
func writeWithBackup(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak-"+time.Now().Format("20060102-150405"), old, 0o600); err != nil {
			return err
		}
		baks, _ := filepath.Glob(path + ".bak-*")
		sort.Strings(baks)
		for len(baks) > 10 {
			os.Remove(baks[0])
			baks = baks[1:]
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
