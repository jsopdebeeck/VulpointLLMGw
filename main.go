package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed dashboard.html
var dashboardHTML []byte

//go:embed chat.html
var chatHTML []byte

//go:embed i18n.js
var i18nJS []byte

// documentation (administration and chat user guide), logo
//
//go:embed docs
var docsFS embed.FS

// Version of LLMGW; every release is described in RELEASE_NOTES.md.
const Version = "1.3.0"

var sessKey []byte

func main() {
	cfgPath := flag.String("config", "config.toml", "fichier de configuration")
	check := flag.Bool("check", false, "valide la configuration et quitte")
	showVersion := flag.Bool("version", false, "affiche la version et quitte")
	flag.Parse()
	if *showVersion {
		fmt.Println("LLMGW by Vulpoint.com", Version)
		return
	}
	dashboardHTML = bytes.ReplaceAll(dashboardHTML, []byte("__LLMGW_VERSION__"), []byte(Version))
	chatHTML = bytes.ReplaceAll(chatHTML, []byte("__LLMGW_VERSION__"), []byte(Version))

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("configuration : %v", err)
	}
	if *check {
		fmt.Printf("configuration OK : %d cibles, %d profils, %d utilisateurs, %d détecteurs\n",
			len(cfg.Targets), len(cfg.Profiles), len(cfg.Users), len(cfg.dets))
		return
	}
	if cfg.Server.AdminToken == "" && len(cfg.Auth.Providers) == 0 {
		log.Print("ATTENTION : admin_token vide et aucun SSO, le dashboard et l'API d'admin sont ouverts")
	}
	for _, p := range cfg.Auth.Providers {
		log.Printf("SSO %s (%s) : adresse de retour à déclarer : %s/auth/callback/%s", p.ID, p.Type, cfg.Auth.BaseURL, p.ID)
		if p.secret == "" {
			log.Printf("ATTENTION : SSO %s sans code secret (%s vide) : la connexion échouera", p.ID, p.ClientSecretEnv)
		}
	}
	store, err := OpenStore(cfg.dbPath)
	if err != nil {
		log.Fatalf("base de métriques : %v", err)
	}
	if learner, err = NewLearner(store.db); err != nil {
		log.Fatalf("mémoire des noms : %v", err)
	}
	if g0, err := metaKey(store.db, "session_key"); err != nil {
		log.Fatalf("clé de session : %v", err)
	} else {
		sessKey = g0
	}
	meter := NewMeter()
	if err := meter.Preload(store); err != nil {
		log.Printf("quotas : préchargement impossible : %v", err)
	}
	g := &Gateway{store: store, meter: meter, sessKey: sessKey, client: &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: time.Duration(cfg.Server.UpstreamTimeout) * time.Second,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}}}
	g.cfg.Store(cfg)
	g.alerts = NewAlerter(g)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go g.watch(ctx, *cfgPath)
	go g.maintenance(ctx)
	go g.alerts.Run(ctx.Done())

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", g.handleLLM("openai"))
	mux.HandleFunc("POST /v1/messages", g.handleLLM("anthropic"))
	mux.HandleFunc("POST /v1/messages/count_tokens", g.handleCountTokens)
	mux.HandleFunc("GET /v1/models", g.handleModels)
	mux.HandleFunc("GET /v1/me", g.handleMe)
	mux.HandleFunc("GET /auth/providers", g.handleAuthProviders)
	mux.HandleFunc("GET /auth/login/{id}", g.handleAuthLogin)
	mux.HandleFunc("GET /auth/callback/{id}", g.handleAuthCallback)
	mux.HandleFunc("GET /auth/session", g.handleAuthSession)
	mux.HandleFunc("POST /auth/logout", g.handleAuthLogout)
	mux.HandleFunc("GET /chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(chatHTML)
	})
	mux.HandleFunc("GET /i18n.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(i18nJS)
	})
	docs, _ := fs.Sub(docsFS, "docs")
	mux.Handle("GET /docs/", http.StripPrefix("/docs/", http.FileServerFS(docs)))
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/docs/", http.StatusFound) })
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]string{"version": Version}) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/dashboard", http.StatusFound) })
	mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(dashboardHTML)
	})
	mux.HandleFunc("GET /admin/api/me", g.staff(g.handleWhoami))
	mux.HandleFunc("GET /admin/api/audit", g.admin(g.handleAudit))
	mux.HandleFunc("GET /admin/api/files-log", g.staff(g.handleFileLog))
	mux.HandleFunc("GET /admin/api/stats", g.staff(g.handleStats))
	mux.HandleFunc("GET /admin/api/config", g.staff(g.handleGetConfig))
	mux.HandleFunc("PUT /admin/api/config", g.staff(g.handlePutConfig)) // support: Users section only
	mux.HandleFunc("GET /admin/api/usage", g.staff(g.handleUsage))
	mux.HandleFunc("POST /admin/api/target-test", g.admin(g.handleTargetTest))
	mux.HandleFunc("POST /admin/api/alert-test", g.admin(func(w http.ResponseWriter, r *http.Request) {
		ac := g.C().Alerts
		var to []string
		to = append(to, ac.To...)
		for _, p := range g.C().Profiles {
			to = append(to, p.AlertEmails...)
		}
		if len(to) == 0 {
			http.Error(w, "aucun destinataire configuré", 400)
			return
		}
		seen := map[string]bool{}
		var uniq []string
		for _, t := range to {
			if !seen[t] {
				seen[t] = true
				uniq = append(uniq, t)
			}
		}
		g.audit(g.actor(r), "test des alertes", strings.Join(uniq, ", "), "")
		if err := sendMail(ac, uniq, "[llmgw] Test des alertes", "Ceci est un message de test du gateway LLM.\nLes alertes arriveront à cette adresse.\n"); err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		writeJSON(w, map[string]any{"sent": uniq})
	}))
	mux.HandleFunc("GET /admin/api/debug", g.guard(roleDebug, g.handleDebugList))
	mux.HandleFunc("GET /admin/api/debug/global", g.guard(roleDebug, g.handleDebugGlobal))
	mux.HandleFunc("GET /admin/api/debug/{id}", g.guard(roleDebug, g.handleDebugGet))
	mux.HandleFunc("DELETE /admin/api/debug", g.admin(g.handleDebugClear))
	mux.HandleFunc("GET /admin/api/learned", g.admin(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"count": learner.Count()})
	}))
	mux.HandleFunc("DELETE /admin/api/learned", g.admin(func(w http.ResponseWriter, r *http.Request) {
		if err := learner.Forget(0); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		g.audit(g.actor(r), "mémoire des noms vidée", "", "")
		writeJSON(w, map[string]any{"count": learner.Count()})
	}))
	mux.HandleFunc("POST /admin/api/test", g.admin(g.handleTest))
	mux.HandleFunc("POST /admin/api/reload", g.admin(func(w http.ResponseWriter, r *http.Request) {
		if err := g.reload(*cfgPath); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		g.audit(g.actor(r), "configuration rechargée", "", "")
		w.Write([]byte("ok"))
	}))

	srv := &http.Server{Addr: cfg.Server.Listen, Handler: mux, ReadHeaderTimeout: 15 * time.Second}
	go func() {
		log.Printf("LLMGW %s à l'écoute sur http://%s (dashboard : /dashboard, documentation : /docs)", Version, cfg.Server.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	log.Print("arrêt en cours…")
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(sctx)
	store.Close()
}

func (g *Gateway) reload(path string) error {
	cfg, err := LoadConfig(path)
	if err != nil {
		return err
	}
	g.cfg.Store(cfg)
	log.Print("configuration rechargée")
	return nil
}

// watch reloads config.toml when it changes (listen, db and timeouts need a restart).
func (g *Gateway) watch(ctx context.Context, path string) {
	var last time.Time
	if st, err := os.Stat(path); err == nil {
		last = st.ModTime()
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st, err := os.Stat(path)
			if err != nil || !st.ModTime().After(last) {
				continue
			}
			last = st.ModTime()
			if err := g.reload(path); err != nil {
				log.Printf("configuration invalide, ancienne conservée : %v", err)
			}
		}
	}
}

func (g *Gateway) maintenance(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		s := g.C().Server
		if err := g.store.Rollup(s.RawRetentionDays, s.AggRetentionDays); err != nil {
			log.Print("agrégation : ", err)
		}
		if err := learner.Forget(365); err != nil { // names unused for a year are forgotten
			log.Print("mémoire des noms : ", err)
		}
		g.store.PurgeAudit(400) // audit log kept 400 days
		g.store.PurgeFileLog()  // file log kept 30 days
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---------- admin API ----------

// admin: endpoints reserved to the Admin role (see audit.go for roles).
func (g *Gateway) admin(h http.HandlerFunc) http.HandlerFunc { return g.guard(roleAdmin, h) }
func (g *Gateway) staff(h http.HandlerFunc) http.HandlerFunc { return g.guard(roleStaff, h) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(encodeJSON(v))
}

func (g *Gateway) handleStats(w http.ResponseWriter, r *http.Request) {
	res, err := g.store.Stats(r.URL.Query().Get("range"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, res)
}

func (g *Gateway) handleTest(w http.ResponseWriter, r *http.Request) {
	var in struct{ Profile, Text string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	cfg := g.C()
	prof := cfg.Profiles[in.Profile]
	if prof == nil {
		http.Error(w, "profil inconnu", 400)
		return
	}
	s := NewSession(cfg, prof)
	s.NoLearn = true // testing must not feed the name memory
	out := s.AnonymizeText(in.Text)
	writeJSON(w, map[string]any{"output": out, "stats": s.Stats, "mapping": s.rev, "blocked": s.Blocked > 0,
		"restored_ok": s.Restore(out) == in.Text || s.Blocked > 0})
}
