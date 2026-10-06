package main

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Email alerts: violations, quotas and LLM errors, sent as a digest every interval_minutes
// to the profile's alert_emails plus [alerts] to. Never contains the sensitive values.

type AlertsCfg struct {
	Enabled         bool     `toml:"enabled"`
	SMTPHost        string   `toml:"smtp_host,omitempty"`
	SMTPPort        int      `toml:"smtp_port,omitempty,omitzero"` // 587 STARTTLS (default), 465 TLS, 25
	SMTPUser        string   `toml:"smtp_user,omitempty"`
	SMTPPassword    string   `toml:"smtp_password,omitempty"`
	SMTPPasswordEnv string   `toml:"smtp_password_env,omitempty"`
	From            string   `toml:"from,omitempty"`
	To              []string `toml:"to,omitempty"` // default recipients, for every profile
	IntervalMinutes int      `toml:"interval_minutes,omitempty,omitzero"`
	OnViolation     bool     `toml:"on_violation"`
	OnQuota         bool     `toml:"on_quota"`
	OnError         bool     `toml:"on_error"`

	password string
}

type Alert struct {
	TS                                   time.Time
	Kind                                 string // violation | quota | error
	User, Profile, Target, Model, Detail string
}

var alertLabels = map[string]string{"file": "Fichier bloqué", "violation": "Violation (requête bloquée)", "quota": "Quota atteint", "error": "Erreur LLM"}

type Alerter struct {
	g       *Gateway
	mu      sync.Mutex
	pending map[string][]Alert // recipient -> alerts
	last    time.Time
}

func NewAlerter(g *Gateway) *Alerter {
	return &Alerter{g: g, pending: map[string][]Alert{}, last: time.Now()}
}

// FromRec turns a finished request into an alert, if it deserves one.
// files: refused files whose rule is block_alert; they are reported even if on_violation is off.
func (a *Alerter) FromRec(rec *Rec, kinds string, files []string) {
	cfg := a.g.C()
	ac := cfg.Alerts
	if !ac.Enabled {
		return
	}
	al := Alert{TS: rec.TS, User: rec.User, Profile: rec.Profile, Target: rec.Target, Model: rec.Model}
	switch {
	case len(files) > 0:
		al.Kind, al.Detail = "file", "fichier(s) bloqué(s) : "+strings.Join(files, ", ")
	case rec.Err == "policy" && ac.OnViolation:
		al.Kind, al.Detail = "violation", "données bloquées : "+kinds
	case rec.Err == "quota" && ac.OnQuota:
		al.Kind, al.Detail = "quota", "quota atteint ("+kinds+")"
	case ac.OnError && rec.Blocked == 0 && (rec.Status == 0 || rec.Status >= 500 || (rec.Status == 401 || rec.Status == 403) && strings.Contains(rec.Err, "upstream")):
		al.Kind, al.Detail = "error", fmt.Sprintf("HTTP %d %s", rec.Status, truncKB(rec.Err, 1))
	default:
		return
	}
	var to []string
	if p := cfg.Profiles[rec.Profile]; p != nil {
		to = append(to, p.AlertEmails...)
	}
	to = append(to, ac.To...)
	a.mu.Lock()
	defer a.mu.Unlock()
	seen := map[string]bool{}
	for _, r := range to {
		if r = strings.TrimSpace(r); r != "" && !seen[r] {
			seen[r] = true
			if len(a.pending[r]) < 500 {
				a.pending[r] = append(a.pending[r], al)
			}
		}
	}
}

func (a *Alerter) Run(stop <-chan struct{}) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			a.flush()
			return
		case <-t.C:
			iv := a.g.C().Alerts.IntervalMinutes
			if time.Since(a.last) >= time.Duration(max(iv, 1))*time.Minute {
				a.flush()
			}
		}
	}
}

func (a *Alerter) flush() {
	a.mu.Lock()
	pending := a.pending
	a.pending, a.last = map[string][]Alert{}, time.Now()
	a.mu.Unlock()
	ac := a.g.C().Alerts
	for to, als := range pending {
		subj, body := digest(als)
		if err := sendMail(ac, []string{to}, subj, body); err != nil {
			log.Printf("alertes : envoi à %s impossible : %v", to, err)
		} else {
			log.Printf("alertes : %d alerte(s) envoyée(s) à %s", len(als), to)
		}
	}
}

func digest(als []Alert) (string, string) {
	n := map[string]int{}
	for _, a := range als {
		n[a.Kind]++
	}
	var parts []string
	for _, k := range []string{"violation", "quota", "error"} {
		if n[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n[k], strings.ToLower(alertLabels[k])))
		}
	}
	subj := "[llmgw] " + strings.Join(parts, ", ")
	sort.Slice(als, func(i, j int) bool { return als[i].TS.Before(als[j].TS) })
	var b strings.Builder
	fmt.Fprintf(&b, "Gateway LLM : %d alerte(s) entre %s et %s.\n\n", len(als), als[0].TS.Format("02/01 15:04"), als[len(als)-1].TS.Format("02/01 15:04"))
	for _, a := range als {
		fmt.Fprintf(&b, "%s  %-28s  utilisateur %s (profil %s)  →  %s/%s\n    %s\n", a.TS.Format("02/01 15:04:05"), alertLabels[a.Kind],
			a.User, a.Profile, a.Target, a.Model, a.Detail)
	}
	b.WriteString("\nAucune valeur sensible n'est incluse dans ce message. Détail : dashboard, onglets Reporting et Debug.\n")
	return subj, b.String()
}

func sendMail(ac AlertsCfg, to []string, subj, body string) error {
	if ac.SMTPHost == "" || ac.From == "" {
		return fmt.Errorf("smtp_host et from requis")
	}
	port := ac.SMTPPort
	if port == 0 {
		port = 587
	}
	addr := net.JoinHostPort(ac.SMTPHost, fmt.Sprint(port))
	msg := "From: " + ac.From + "\r\nTo: " + strings.Join(to, ", ") + "\r\nSubject: " + mimeHeader(subj) +
		"\r\nDate: " + time.Now().Format(time.RFC1123Z) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\n" + strings.ReplaceAll(body, "\n", "\r\n")
	var auth smtp.Auth
	if ac.SMTPUser != "" {
		auth = smtp.PlainAuth("", ac.SMTPUser, ac.password, ac.SMTPHost)
	}
	if port != 465 {
		return smtp.SendMail(addr, auth, ac.From, to, []byte(msg)) // STARTTLS when offered
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, &tls.Config{ServerName: ac.SMTPHost})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, ac.SMTPHost)
	if err != nil {
		return err
	}
	defer c.Close()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(ac.From); err != nil {
		return err
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func mimeHeader(s string) string {
	for _, r := range s {
		if r > 127 {
			return "=?UTF-8?B?" + b64(s) + "?="
		}
	}
	return s
}
