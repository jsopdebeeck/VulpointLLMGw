package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the whole gateway configuration (config.toml). It is immutable once
// loaded; hot reload swaps the pointer atomically.
type Config struct {
	Server    ServerCfg           `toml:"server"`
	Targets   map[string]*Target  `toml:"targets"`
	Profiles  map[string]*Profile `toml:"profiles"`
	Users     map[string]*User    `toml:"users"`
	Privacy   PrivacyCfg          `toml:"privacy"`
	Detectors []*DetectorCfg      `toml:"detectors"`

	allow  map[string]bool   // folded terms never anonymized
	keys   map[string]string // client API key -> user name
	dets   []*Detector
	dbPath string
	path   string
}

type ServerCfg struct {
	Listen           string `toml:"listen"`
	DB               string `toml:"db"`
	AdminToken       string `toml:"admin_token,omitempty"`
	RawRetentionDays int    `toml:"raw_retention_days"`
	AggRetentionDays int    `toml:"agg_retention_days"`
	MaxBodyMB        int    `toml:"max_body_mb"`
	UpstreamTimeout  int    `toml:"upstream_timeout_sec"`
}

type Target struct {
	Description string            `toml:"description,omitempty"`
	API         string            `toml:"api"` // openai | anthropic
	BaseURL     string            `toml:"base_url"`
	APIKey      string            `toml:"api_key,omitempty"`
	APIKeyEnv   string            `toml:"api_key_env,omitempty"`
	Models      []string          `toml:"models,omitempty"` // glob, "*" = any
	Headers     map[string]string `toml:"headers,omitempty"`
	StreamUsage bool              `toml:"stream_usage,omitempty"` // ask OpenAI-style servers for usage in streams
	Disabled    bool              `toml:"disabled,omitempty"`
	Quota       *Quota            `toml:"quota,omitempty"` // shared by all users of this target

	key  string
	pats []*regexp.Regexp
}

func (t *Target) Allows(model string) bool {
	if len(t.pats) == 0 {
		return true
	}
	for _, p := range t.pats {
		if p.MatchString(model) {
			return true
		}
	}
	return false
}

type Profile struct {
	Description   string            `toml:"description,omitempty"`
	Targets       []string          `toml:"targets"` // allowed LLMs, in priority / fallback order
	Images        string            `toml:"images"`  // pass | strip | block
	DefaultAction string            `toml:"default_action"`
	Actions       map[string]string `toml:"actions,omitempty"` // detector kind -> anonymize | block | log | off
	Hint          bool              `toml:"placeholder_hint,omitempty"`
	Quota         *Quota            `toml:"quota,omitempty"` // per user of this profile
}

type User struct {
	Description string `toml:"description,omitempty"`
	Kind        string `toml:"kind,omitempty"` // user | app
	Key         string `toml:"key"`
	Profile     string `toml:"profile"`
	Disabled    bool   `toml:"disabled,omitempty"`
	Quota       *Quota `toml:"quota,omitempty"` // overrides the profile quota field by field (-1 = unlimited)
}

// Quota limits; 0 = inherit / no limit.
type Quota struct {
	RPM         int64  `toml:"rpm,omitempty,omitzero"`
	RPD         int64  `toml:"rpd,omitempty,omitzero"`
	TokensDay   int64  `toml:"tokens_day,omitempty,omitzero"`
	TokensMonth int64  `toml:"tokens_month,omitempty,omitzero"`
	Action      string `toml:"action,omitempty"` // block (default) | warn
}

type PrivacyCfg struct {
	Allow []string `toml:"allow,omitempty"` // never anonymized (products, public figures…)
}

type DetectorCfg struct {
	Name        string   `toml:"name"`
	Description string   `toml:"description,omitempty"`
	Builtin     string   `toml:"builtin,omitempty"`
	Regex       string   `toml:"regex,omitempty"`
	Group       int      `toml:"group,omitempty,omitzero"`
	Words       []string `toml:"words,omitempty"`
	WordsFile   string   `toml:"words_file,omitempty"`
}

var (
	validActions = map[string]bool{"anonymize": true, "block": true, "log": true, "off": true}
	validMedia   = map[string]bool{"pass": true, "strip": true, "block": true}
	kindRe       = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := ParseConfig(data, filepath.Dir(path))
	if c != nil {
		c.path = path
	}
	return c, err
}

func ParseConfig(data []byte, base string) (*Config, error) {
	var c Config
	md, err := toml.Decode(string(data), &c)
	if err != nil {
		return nil, err
	}
	if u := md.Undecoded(); len(u) > 0 {
		return nil, fmt.Errorf("clés inconnues : %v", u)
	}
	s := &c.Server
	if s.Listen == "" {
		s.Listen = "127.0.0.1:8080"
	}
	if s.DB == "" {
		s.DB = "gateway.db"
	}
	c.dbPath = s.DB
	if !filepath.IsAbs(s.DB) {
		c.dbPath = filepath.Join(base, s.DB)
	}
	if s.RawRetentionDays <= 0 {
		s.RawRetentionDays = 30
	}
	if s.AggRetentionDays <= 0 {
		s.AggRetentionDays = 3*366 + 31
	}
	if s.MaxBodyMB <= 0 {
		s.MaxBodyMB = 50
	}
	if s.UpstreamTimeout <= 0 {
		s.UpstreamTimeout = 300
	}

	for n, t := range c.Targets {
		if t.API != "openai" && t.API != "anthropic" {
			return nil, fmt.Errorf("target %q : api doit valoir openai ou anthropic", n)
		}
		if t.BaseURL == "" {
			return nil, fmt.Errorf("target %q : base_url manquant", n)
		}
		t.key = os.ExpandEnv(t.APIKey)
		if t.APIKeyEnv != "" {
			t.key = os.Getenv(t.APIKeyEnv)
		}
		if err := checkQuota(t.Quota); err != nil {
			return nil, fmt.Errorf("target %q : %w", n, err)
		}
		for _, m := range t.Models {
			t.pats = append(t.pats, regexp.MustCompile("^"+strings.ReplaceAll(regexp.QuoteMeta(m), `\*`, ".*")+"$"))
		}
	}

	c.allow = map[string]bool{}
	for _, a := range c.Privacy.Allow {
		if a = strings.TrimSpace(a); a != "" {
			c.allow[fold(a)] = true
		}
	}
	c.dets = nil
	for i, dc := range c.Detectors {
		d, err := buildDetector(dc, base, i)
		if err != nil {
			return nil, fmt.Errorf("détecteur %q : %w", dc.Name, err)
		}
		c.dets = append(c.dets, d)
	}

	for n, p := range c.Profiles {
		if len(p.Targets) == 0 {
			return nil, fmt.Errorf("profil %q : aucune cible autorisée", n)
		}
		for _, t := range p.Targets {
			if c.Targets[t] == nil {
				return nil, fmt.Errorf("profil %q : cible inconnue %q", n, t)
			}
		}
		if err := checkQuota(p.Quota); err != nil {
			return nil, fmt.Errorf("profil %q : %w", n, err)
		}
		if p.Images == "" {
			p.Images = "strip"
		}
		if !validMedia[p.Images] {
			return nil, fmt.Errorf("profil %q : images doit valoir pass, strip ou block", n)
		}
		if p.DefaultAction == "" {
			p.DefaultAction = "anonymize"
		}
		if !validActions[p.DefaultAction] {
			return nil, fmt.Errorf("profil %q : default_action invalide", n)
		}
		acts := map[string]string{}
		for k, a := range p.Actions {
			if !validActions[a] {
				return nil, fmt.Errorf("profil %q : action %q invalide pour %s", n, a, k)
			}
			acts[strings.ToUpper(k)] = a
		}
		p.Actions = acts
	}

	c.keys = map[string]string{}
	for n, u := range c.Users {
		if c.Profiles[u.Profile] == nil {
			return nil, fmt.Errorf("utilisateur %q : profil inconnu %q", n, u.Profile)
		}
		if u.Kind != "" && u.Kind != "user" && u.Kind != "app" {
			return nil, fmt.Errorf("utilisateur %q : kind doit valoir user ou app", n)
		}
		if err := checkQuota(u.Quota); err != nil {
			return nil, fmt.Errorf("utilisateur %q : %w", n, err)
		}
		k := os.ExpandEnv(u.Key)
		if k == "" {
			return nil, fmt.Errorf("utilisateur %q : clé vide", n)
		}
		if o, dup := c.keys[k]; dup {
			return nil, fmt.Errorf("utilisateurs %q et %q : même clé", o, n)
		}
		c.keys[k] = n
	}
	return &c, nil
}

func readWords(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out, sc.Err()
}

func checkQuota(q *Quota) error {
	if q != nil && q.Action != "" && q.Action != "block" && q.Action != "warn" {
		return fmt.Errorf("quota : action doit valoir block ou warn")
	}
	return nil
}

// EffectiveQuota merges a user's overrides onto the profile quota (-1 = unlimited).
func (c *Config) EffectiveQuota(u *User) Quota {
	var q Quota
	if p := c.Profiles[u.Profile]; p != nil && p.Quota != nil {
		q = *p.Quota
	}
	if o := u.Quota; o != nil {
		pick := func(dst *int64, v int64) {
			if v != 0 {
				*dst = max(v, 0)
			}
		}
		pick(&q.RPM, o.RPM)
		pick(&q.RPD, o.RPD)
		pick(&q.TokensDay, o.TokensDay)
		pick(&q.TokensMonth, o.TokensMonth)
		if o.Action != "" {
			q.Action = o.Action
		}
	}
	return q
}
