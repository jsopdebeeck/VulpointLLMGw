package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Web shield: protection of a gateway exposed to the Internet (VPS), with or without a
// reverse proxy. Real client address behind trusted proxies, rate limit and concurrency cap
// per address, temporary ban after repeated failed sign-ins, admin address allow list,
// security headers, and a cap on concurrent anonymization work (CPU).

type SecurityCfg struct {
	TrustedProxies []string `toml:"trusted_proxies,omitempty"` // proxies whose X-Forwarded-For is believed (default: loopback)
	AdminAllow     []string `toml:"admin_allow,omitempty"`     // addresses allowed on /dashboard and /admin ("" = all)
	Exempt         []string `toml:"exempt,omitempty"`          // never limited nor banned (internal networks)
	RatePerMin     int      `toml:"rate_per_min,omitempty,omitzero"`
	MaxConcurrent  int      `toml:"max_concurrent_per_ip,omitempty,omitzero"`
	BanAfter       int      `toml:"ban_after,omitempty,omitzero"`      // failed sign-ins within ban_window_min
	BanWindowMin   int      `toml:"ban_window_min,omitempty,omitzero"` //
	BanMinutes     int      `toml:"ban_minutes,omitempty,omitzero"`    // doubled at each new ban, 24 h at most
	TLSCert        string   `toml:"tls_cert,omitempty"`                // built-in HTTPS (PEM files), without reverse proxy
	TLSKey         string   `toml:"tls_key,omitempty"`
	Disabled       bool     `toml:"disabled,omitempty"` // no rate limit nor ban (headers and allow list stay)

	trusted, admin, exempt []*net.IPNet
}

func (s *SecurityCfg) check(base string) error {
	if s.RatePerMin <= 0 {
		s.RatePerMin = 600
	}
	if s.MaxConcurrent <= 0 {
		s.MaxConcurrent = 32
	}
	if s.BanAfter <= 0 {
		s.BanAfter = 10
	}
	if s.BanWindowMin <= 0 {
		s.BanWindowMin = 10
	}
	if s.BanMinutes <= 0 {
		s.BanMinutes = 30
	}
	if (s.TLSCert == "") != (s.TLSKey == "") {
		return fmt.Errorf("security : tls_cert et tls_key vont ensemble")
	}
	for _, p := range []*string{&s.TLSCert, &s.TLSKey} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(base, *p)
		}
	}
	var err error
	trusted := s.TrustedProxies
	if len(trusted) == 0 {
		trusted = []string{"127.0.0.1/32", "::1/128"}
	}
	for _, l := range []struct {
		name string
		in   []string
		out  *[]*net.IPNet
	}{{"trusted_proxies", trusted, &s.trusted}, {"admin_allow", s.AdminAllow, &s.admin}, {"exempt", s.Exempt, &s.exempt}} {
		if *l.out, err = parseNets(l.in); err != nil {
			return fmt.Errorf("security : %s : %w", l.name, err)
		}
	}
	return nil
}

// parseNets accepts CIDR ranges and bare addresses.
func parseNets(in []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if !strings.Contains(v, "/") {
			if ip := net.ParseIP(v); ip != nil {
				if ip.To4() != nil {
					v += "/32"
				} else {
					v += "/128"
				}
			}
		}
		_, n, err := net.ParseCIDR(v)
		if err != nil {
			return nil, fmt.Errorf("adresse ou plage invalide %q", v)
		}
		out = append(out, n)
	}
	return out, nil
}

func inNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ---------- client address ----------

type ctxKey int

const reqInfoKey ctxKey = 0

// reqInfo travels with each request: the real client address, and whether the handler
// saw a failed sign-in (counted towards a ban).
type reqInfo struct {
	ip     string
	failed bool
	https  bool
}

func info(r *http.Request) *reqInfo {
	ri, _ := r.Context().Value(reqInfoKey).(*reqInfo)
	return ri
}

// clientIP: the address of the caller, behind the trusted proxies when there are some.
func clientIP(r *http.Request) string {
	if ri := info(r); ri != nil {
		return ri.ip
	}
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// authFailed marks the request as a failed sign-in (bad key, bad admin token, forged cookie…).
func authFailed(r *http.Request) {
	if ri := info(r); ri != nil {
		ri.failed = true
	}
}

// realIP walks X-Forwarded-For from the right: the first address that is not a trusted
// proxy is the client (anything further left could have been written by the client).
func realIP(r *http.Request, trusted []*net.IPNet) (string, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !inNets(ip, trusted) {
		return host, r.TLS != nil
	}
	https := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		pip := net.ParseIP(p)
		if pip == nil {
			break
		}
		if !inNets(pip, trusted) {
			return pip.String(), https
		}
		host = pip.String()
	}
	return host, https
}

// bucket: one entry per IPv4 address, per /64 for IPv6 (an attacker easily owns a whole /64).
func bucket(ip string) string {
	p := net.ParseIP(ip)
	if p == nil || p.To4() != nil {
		return ip
	}
	return p.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// ---------- per-address state ----------

type ipState struct {
	tokens    float64
	last      time.Time
	active    int
	failN     int
	failStart time.Time
	banUntil  time.Time
	bans      int
	lastBan   time.Time
	reason    string
}

type Shield struct {
	mu  sync.Mutex
	ips map[string]*ipState
	cpu chan struct{} // concurrent anonymization work
}

const maxTracked = 200000

func NewShield() *Shield {
	return &Shield{ips: map[string]*ipState{}, cpu: make(chan struct{}, 2*runtime.GOMAXPROCS(0))}
}

func (s *Shield) state(k string, now time.Time) *ipState {
	st := s.ips[k]
	if st == nil {
		if len(s.ips) >= maxTracked {
			s.gc(now, 5*time.Minute)
		}
		st = &ipState{last: now, tokens: -1}
		s.ips[k] = st
	}
	return st
}

// gc forgets the addresses idle for longer than idle (bans and recent bans are kept).
func (s *Shield) gc(now time.Time, idle time.Duration) {
	for k, st := range s.ips {
		if st.active == 0 && now.Sub(st.last) > idle && now.After(st.banUntil) && now.Sub(st.lastBan) > 24*time.Hour {
			delete(s.ips, k)
		}
	}
}

func (s *Shield) run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.mu.Lock()
			s.gc(now, time.Hour)
			s.mu.Unlock()
		}
	}
}

// admit applies the ban and the rate limit; it returns the HTTP status to answer (0 = go on).
func (s *Shield) admit(k string, sc *SecurityCfg, now time.Time) (int, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state(k, now)
	if now.Before(st.banUntil) {
		return 403, st.banUntil.Sub(now)
	}
	rate := float64(sc.RatePerMin)
	if st.tokens < 0 {
		st.tokens = rate
	} else {
		st.tokens = min(rate, st.tokens+now.Sub(st.last).Seconds()*rate/60)
	}
	st.last = now
	if st.tokens < 1 {
		return 429, time.Duration((1-st.tokens)*60/rate*float64(time.Second)) + time.Second
	}
	if st.active >= sc.MaxConcurrent {
		return 429, 5 * time.Second
	}
	st.tokens--
	st.active++
	return 0, 0
}

func (s *Shield) done(k string) {
	s.mu.Lock()
	if st := s.ips[k]; st != nil && st.active > 0 {
		st.active--
	}
	s.mu.Unlock()
}

// fail counts a failed sign-in; it returns the ban duration when this one triggers a ban.
func (s *Shield) fail(k string, sc *SecurityCfg, now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state(k, now)
	if now.Sub(st.failStart) > time.Duration(sc.BanWindowMin)*time.Minute {
		st.failN, st.failStart = 0, now
	}
	st.failN++
	if st.failN < sc.BanAfter || now.Before(st.banUntil) {
		return 0
	}
	if now.Sub(st.lastBan) > 24*time.Hour {
		st.bans = 0
	}
	d := time.Duration(sc.BanMinutes) * time.Minute << min(st.bans, 6)
	d = min(d, 24*time.Hour)
	st.bans++
	st.banUntil, st.lastBan, st.failN = now.Add(d), now, 0
	st.reason = fmt.Sprintf("%d connexions refusées en %d min", sc.BanAfter, sc.BanWindowMin)
	return d
}

// minutes: a ban duration for people ("30 min", "2 h").
func minutes(d time.Duration) string {
	if d >= time.Hour && d%time.Hour == 0 {
		return fmt.Sprintf("%d h", d/time.Hour)
	}
	return fmt.Sprintf("%d min", d/time.Minute)
}

type Ban struct {
	Address string
	Until   int64
	Bans    int
	Reason  string
}

func (s *Shield) Bans() []Ban {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	out := []Ban{}
	for k, st := range s.ips {
		if now.Before(st.banUntil) {
			out = append(out, Ban{k, st.banUntil.Unix(), st.bans, st.reason})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Until > out[j].Until })
	return out
}

func (s *Shield) Unban(k string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.ips[k]; st != nil && time.Now().Before(st.banUntil) {
		st.banUntil, st.failN = time.Time{}, 0
		return true
	}
	return false
}

// work bounds the CPU-heavy anonymization: at most 2 × cores requests at once, the others
// wait (until their client gives up, or 30 s).
func (s *Shield) work(ctx context.Context) (release func(), ok bool) {
	if s == nil {
		return func() {}, true
	}
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	select {
	case s.cpu <- struct{}{}:
		return func() { <-s.cpu }, true
	case <-ctx.Done():
	case <-t.C:
	}
	return nil, false
}

// ---------- middleware ----------

// adminPath: pages and API reserved to the administration (admin_allow).
func adminPath(p string) bool {
	return p == "/dashboard" || strings.HasPrefix(p, "/dashboard/") || strings.HasPrefix(p, "/admin/")
}

const csp = "default-src 'self'; script-src 'self' 'unsafe-inline' https://cdnjs.cloudflare.com; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; img-src 'self' data: blob:; font-src 'self' data: https://fonts.gstatic.com; connect-src 'self'; " +
	"worker-src 'self' blob: https://cdnjs.cloudflare.com; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func (g *Gateway) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc := &g.C().Security
		ip, https := realIP(r, sc.trusted)
		ri := &reqInfo{ip: ip, https: https}
		r = r.WithContext(context.WithValue(r.Context(), reqInfoKey, ri))

		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Content-Security-Policy", csp)
		if https {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}

		pip := net.ParseIP(ip)
		if len(sc.admin) > 0 && adminPath(r.URL.Path) && (pip == nil || !inNets(pip, sc.admin)) {
			http.Error(w, "administration non accessible depuis cette adresse", http.StatusForbidden)
			return
		}
		// local calls (the Mac app, an SSH tunnel) and exempt networks are never limited
		if sc.Disabled || pip == nil || pip.IsLoopback() || inNets(pip, sc.exempt) {
			next.ServeHTTP(w, r)
			return
		}
		k := bucket(ip)
		if st, wait := g.shield.admit(k, sc, time.Now()); st != 0 {
			h.Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
			if st == 403 {
				http.Error(w, "adresse temporairement bloquée après des connexions refusées", st)
			} else {
				http.Error(w, "trop de requêtes depuis cette adresse, réessayez plus tard", st)
			}
			return
		}
		defer g.shield.done(k)
		next.ServeHTTP(w, r)
		if ri.failed {
			if d := g.shield.fail(k, sc, time.Now()); d > 0 {
				log.Printf("sécurité : %s bloquée pendant %s (connexions refusées)", k, minutes(d))
				g.audit(&Actor{Name: "système"}, "adresse bloquée", k, fmt.Sprintf("%d connexions refusées en %d min · blocage %s", sc.BanAfter, sc.BanWindowMin, minutes(d)))
				if g.alerts != nil {
					g.alerts.Security(k, fmt.Sprintf("adresse %s bloquée %s après %d connexions refusées", k, minutes(d), sc.BanAfter))
				}
			}
		}
	})
}

// ---------- administration API ----------

func (g *Gateway) handleSecurity(w http.ResponseWriter, r *http.Request) {
	sc := g.C().Security
	writeJSON(w, map[string]any{"bans": g.shield.Bans(), "you": clientIP(r), "disabled": sc.Disabled,
		"tls": sc.TLSCert != "", "proxied": clientIP(r) != hostOf(r.RemoteAddr)})
}

func (g *Gateway) handleUnban(w http.ResponseWriter, r *http.Request) {
	a := r.URL.Query().Get("address")
	if !g.shield.Unban(a) {
		http.Error(w, "adresse non bloquée", 404)
		return
	}
	g.audit(g.actor(r), "adresse débloquée", a, "")
	writeJSON(w, map[string]any{"ok": true})
}

func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}
