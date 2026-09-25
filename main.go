package main

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed dashboard.html
var dashboardHTML []byte

//go:embed chat.html
var chatHTML []byte

func main() {
	cfgPath := flag.String("config", "config.toml", "fichier de configuration")
	check := flag.Bool("check", false, "valide la configuration et quitte")
	flag.Parse()

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("configuration : %v", err)
	}
	if *check {
		fmt.Printf("configuration OK : %d cibles, %d profils, %d utilisateurs, %d détecteurs\n",
			len(cfg.Targets), len(cfg.Profiles), len(cfg.Users), len(cfg.dets))
		return
	}
	if cfg.Server.AdminToken == "" {
		log.Print("ATTENTION : admin_token vide, le dashboard et l'API d'admin sont ouverts")
	}
	store, err := OpenStore(cfg.dbPath)
	if err != nil {
		log.Fatalf("base de métriques : %v", err)
	}
	if learner, err = NewLearner(store.db); err != nil {
		log.Fatalf("mémoire des noms : %v", err)
	}
	meter := NewMeter()
	if err := meter.Preload(store); err != nil {
		log.Printf("quotas : préchargement impossible : %v", err)
	}
	g := &Gateway{store: store, meter: meter, client: &http.Client{Transport: &http.Transport{
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
	mux.HandleFunc("GET /chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(chatHTML)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/dashboard", http.StatusFound) })
	mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(dashboardHTML)
	})
	mux.HandleFunc("GET /admin/api/stats", g.admin(g.handleStats))
	mux.HandleFunc("GET /admin/api/config", g.admin(g.handleGetConfig))
	mux.HandleFunc("PUT /admin/api/config", g.admin(g.handlePutConfig))
	mux.HandleFunc("GET /admin/api/usage", g.admin(g.handleUsage))
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
		if err := sendMail(ac, uniq, "[llmgw] Test des alertes", "Ceci est un message de test du gateway LLM.\nLes alertes arriveront à cette adresse.\n"); err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		writeJSON(w, map[string]any{"sent": uniq})
	}))
	mux.HandleFunc("GET /admin/api/debug", g.admin(g.handleDebugList))
	mux.HandleFunc("GET /admin/api/debug/global", g.admin(g.handleDebugGlobal))
	mux.HandleFunc("GET /admin/api/debug/{id}", g.admin(g.handleDebugGet))
	mux.HandleFunc("DELETE /admin/api/debug", g.admin(g.handleDebugClear))
	mux.HandleFunc("GET /admin/api/learned", g.admin(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"count": learner.Count()})
	}))
	mux.HandleFunc("DELETE /admin/api/learned", g.admin(func(w http.ResponseWriter, r *http.Request) {
		if err := learner.Forget(0); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]any{"count": learner.Count()})
	}))
	mux.HandleFunc("POST /admin/api/test", g.admin(g.handleTest))
	mux.HandleFunc("POST /admin/api/reload", g.admin(func(w http.ResponseWriter, r *http.Request) {
		if err := g.reload(*cfgPath); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Write([]byte("ok"))
	}))

	srv := &http.Server{Addr: cfg.Server.Listen, Handler: mux, ReadHeaderTimeout: 15 * time.Second}
	go func() {
		log.Printf("llmgw à l'écoute sur http://%s (dashboard : /dashboard)", cfg.Server.Listen)
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
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---------- admin API ----------

func (g *Gateway) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tok := g.C().Server.AdminToken; tok != "" &&
			subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Admin-Token")), []byte(tok)) != 1 {
			http.Error(w, "jeton d'administration requis", 401)
			return
		}
		h(w, r)
	}
}

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
