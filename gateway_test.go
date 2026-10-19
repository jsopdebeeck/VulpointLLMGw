package main

import (
	"bufio"
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const pii = "Bonjour, je suis Mme Claire Dubois (claire.dubois@acme.be, +32 470 12 34 56). " +
	"NISS 90.01.01-123.95, dossier DOS-2025-00042 chez ACME SA, IBAN BE68 5390 0754 7034. Dubois confirme."

var secrets = []string{"Claire", "Dubois", "claire.dubois@acme.be", "470 12 34 56", "123.95", "DOS-2025-00042", "ACME", "BE68"}

// mock upstream: records the request and echoes the last user message back.
type mock struct {
	mu     sync.Mutex
	bodies []string
	fail   int // status to return (0 = OK)
}

func (m *mock) last() string { m.mu.Lock(); defer m.mu.Unlock(); return m.bodies[len(m.bodies)-1] }

func lastUserText(body map[string]any) string {
	msgs, _ := body["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		mm := msgs[i].(map[string]any)
		if mm["role"] != "user" {
			continue
		}
		switch c := mm["content"].(type) {
		case string:
			return c
		case []any:
			var b strings.Builder
			for _, p := range c {
				if t, ok := p.(map[string]any)["text"].(string); ok {
					b.WriteString(t)
				}
			}
			return b.String()
		}
	}
	return ""
}

func chunks(s string, n int) []string {
	var out []string
	for len(s) > 0 {
		k := min(n, len(s))
		for k < len(s) && !utf8Start(s[k]) {
			k++
		}
		out = append(out, s[:k])
		s = s[k:]
	}
	return out
}
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func (m *mock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.bodies = append(m.bodies, string(raw))
	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"input_tokens":%d}`, len(raw)/4)
		return
	}
	fail := m.fail
	m.mu.Unlock()
	if fail != 0 {
		w.WriteHeader(fail)
		return
	}
	var body map[string]any
	json.Unmarshal(raw, &body)
	echo := lastUserText(body)
	stream, _ := body["stream"].(bool)
	anth := strings.HasSuffix(r.URL.Path, "/v1/messages")
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		if anth {
			json.NewEncoder(w).Encode(map[string]any{"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "text", "text": echo}}, "usage": map[string]any{"input_tokens": 11, "output_tokens": 22}})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"id": "x", "object": "chat.completion", "choices": []any{map[string]any{"index": 0,
				"message": map[string]any{"role": "assistant", "content": echo}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 22}})
		}
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	ev := func(name string, v any) {
		b, _ := json.Marshal(v)
		if name != "" {
			fmt.Fprintf(w, "event: %s\n", name)
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		w.(http.Flusher).Flush()
	}
	if anth {
		ev("message_start", map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]any{"input_tokens": 11, "output_tokens": 1}}})
		ev("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		for _, c := range chunks(echo, 3) {
			ev("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": c}})
		}
		ev("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		ev("message_delta", map[string]any{"type": "message_delta", "usage": map[string]any{"output_tokens": 22}})
		ev("message_stop", map[string]any{"type": "message_stop"})
		return
	}
	for _, c := range chunks(echo, 3) {
		ev("", map[string]any{"id": "x", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": c}, "finish_reason": nil}}})
	}
	ev("", map[string]any{"id": "x", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
	ev("", map[string]any{"id": "x", "choices": []any{}, "usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 22}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func setup(t *testing.T) (*httptest.Server, *mock, *mock, *Gateway) {
	t.Helper()
	oai, ant := &mock{}, &mock{}
	so, sa := httptest.NewServer(oai), httptest.NewServer(ant)
	t.Cleanup(so.Close)
	t.Cleanup(sa.Close)
	dir := t.TempDir()
	cfgText, _ := os.ReadFile("config.example.toml")
	c := string(cfgText)
	c = strings.Replace(c, "http://localhost:11434/v1", so.URL+"/v1", 1)
	c = strings.Replace(c, "https://api.mistral.ai/v1", so.URL+"/v1", 1)
	c = strings.Replace(c, "https://api.openai.com/v1", so.URL+"/v1", 1)
	c = strings.Replace(c, "https://api.anthropic.com", sa.URL, 1)
	p := filepath.Join(dir, "config.toml")
	os.WriteFile(p, []byte(c), 0o600)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(cfg.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: st, meter: NewMeter(), client: &http.Client{Timeout: 10 * time.Second}}
	g.cfg.Store(cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", g.handleLLM("openai"))
	mux.HandleFunc("POST /v1/messages", g.handleLLM("anthropic"))
	mux.HandleFunc("POST /v1/messages/count_tokens", g.handleCountTokens)
	gw := httptest.NewServer(mux)
	t.Cleanup(gw.Close)
	return gw, oai, ant, g
}

func post(t *testing.T, url, key string, body any) *http.Response {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func noSecrets(t *testing.T, upstream string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(upstream, s) {
			t.Errorf("fuite vers le LLM : %q présent dans %s", s, upstream)
		}
	}
}

func TestAnonymizeRoundTrip(t *testing.T) {
	cfg, err := LoadConfig("config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	s := NewSession(cfg, cfg.Profiles["standard"])
	out := s.AnonymizeText(pii)
	t.Log(out)
	noSecrets(t, out)
	if back := s.Restore(out); back != pii {
		t.Errorf("restauration incorrecte :\n%s\n%s", back, pii)
	}
	if !strings.Contains(out, "«PERSON_") || !strings.Contains(out, "«NISS_1»") {
		t.Errorf("jetons attendus absents : %s", out)
	}
}

func TestRestorerSplit(t *testing.T) {
	cfg, _ := LoadConfig("config.example.toml")
	s := NewSession(cfg, cfg.Profiles["standard"])
	out := s.AnonymizeText("écrire à jean@x.org")
	r := newRestorer(s)
	var got string
	for _, c := range chunks(out, 2) {
		got += r.Push(rkey{}, c)
	}
	got += r.Flush(rkey{})
	if got != "écrire à jean@x.org" {
		t.Errorf("got %q", got)
	}
}

func TestOpenAI(t *testing.T) {
	gw, oai, _, _ := setup(t)
	for _, stream := range []bool{false, true} {
		resp := post(t, gw.URL+"/v1/chat/completions", "gw-alice-remplace-moi", map[string]any{
			"model": "mistral-large", "stream": stream, "messages": []any{map[string]any{"role": "user", "content": pii}}})
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status %d : %s", resp.StatusCode, body)
		}
		noSecrets(t, oai.last())
		var got string
		if stream {
			sc := bufio.NewScanner(bytes.NewReader(body))
			for sc.Scan() {
				l := sc.Text()
				if !strings.HasPrefix(l, "data: {") {
					continue
				}
				var m map[string]any
				json.Unmarshal([]byte(l[6:]), &m)
				for _, c := range m["choices"].([]any) {
					if s, ok := c.(map[string]any)["delta"].(map[string]any)["content"].(string); ok {
						got += s
					}
				}
			}
		} else {
			var m map[string]any
			json.Unmarshal(body, &m)
			got = m["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
		}
		if got != pii {
			t.Errorf("stream=%v réponse restaurée incorrecte :\n%q", stream, got)
		}
		if !strings.Contains(oai.last(), "pseudonymisées") {
			t.Error("consigne placeholder_hint absente")
		}
	}
}

func TestAnthropicStream(t *testing.T) {
	gw, _, ant, _ := setup(t)
	resp := post(t, gw.URL+"/v1/messages", "gw-alice-remplace-moi", map[string]any{
		"model": "claude-sonnet-5", "stream": true, "max_tokens": 100, "system": "Tu es utile.",
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": pii},
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "AAAA"}},
		}}}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d : %s", resp.StatusCode, body)
	}
	up := ant.last()
	noSecrets(t, up)
	if strings.Contains(up, `"AAAA"`) || !strings.Contains(up, whitePNG) {
		t.Error("image non remplacée par le carré blanc")
	}
	var got string
	for _, l := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(l, "data: ") && strings.Contains(l, "text_delta") {
			var m map[string]any
			json.Unmarshal([]byte(l[6:]), &m)
			got += m["delta"].(map[string]any)["text"].(string)
		}
	}
	want := pii
	if got != want {
		t.Errorf("réponse restaurée incorrecte :\n%q\n%q", got, want)
	}
}

func TestBlockAndRouting(t *testing.T) {
	gw, _, _, _ := setup(t)
	// bob (strict) : NISS interdit
	resp := post(t, gw.URL+"/v1/chat/completions", "gw-bob-remplace-moi", map[string]any{
		"model": "mistral-large", "messages": []any{map[string]any{"role": "user", "content": pii}}})
	if resp.StatusCode != 403 {
		t.Errorf("blocage attendu, status %d", resp.StatusCode)
	}
	// bob n'a pas accès à anthropic
	resp = post(t, gw.URL+"/v1/messages", "gw-bob-remplace-moi", map[string]any{"model": "claude-sonnet-5", "max_tokens": 5,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 403 {
		t.Errorf("routage interdit attendu, status %d", resp.StatusCode)
	}
	resp = post(t, gw.URL+"/v1/chat/completions", "mauvaise-cle", map[string]any{"model": "x"})
	if resp.StatusCode != 401 {
		t.Errorf("401 attendu, status %d", resp.StatusCode)
	}
}

func TestFallbackAndStats(t *testing.T) {
	gw, oai, _, g := setup(t)
	oai.fail = 503 // toutes les cibles OpenAI tombent → 502 après repli
	resp := post(t, gw.URL+"/v1/chat/completions", "gw-alice-remplace-moi", map[string]any{
		"model": "mistral-large", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 503 {
		t.Errorf("status %d", resp.StatusCode)
	}
	if n := len(oai.bodies); n != 2 { // mistral puis local (openai n'accepte pas mistral-*)
		t.Errorf("repli : %d tentatives au lieu de 2", n)
	}
	oai.fail = 0
	post(t, gw.URL+"/v1/chat/completions", "gw-alice-remplace-moi", map[string]any{
		"model": "mistral-large", "messages": []any{map[string]any{"role": "user", "content": pii}}})
	time.Sleep(1500 * time.Millisecond)
	for _, rng := range []string{"48h", "60d", "6w", "6m"} {
		res, err := g.store.Stats(rng)
		if err != nil {
			t.Fatal(rng, err)
		}
		rows := res["all"].([]Row)
		var n, an int64
		for _, r := range rows {
			n += r.N
			an += r.An
		}
		if n != 2 || an == 0 {
			t.Errorf("%s : %d requêtes, %d conversions", rng, n, an)
		}
	}
	if err := g.store.Rollup(-1, 1200); err != nil { // tout agréger
		t.Fatal(err)
	}
	res, _ := g.store.Stats("60d")
	var n int64
	for _, r := range res["all"].([]Row) {
		n += r.N
	}
	if n != 2 {
		t.Errorf("après agrégation : %d requêtes", n)
	}
}

func TestQuota(t *testing.T) {
	gw, _, _, g := setup(t)
	g.C().Users["alice"].Quota = &Quota{RPD: 1}
	req := map[string]any{"model": "mistral-large", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	if r := post(t, gw.URL+"/v1/chat/completions", "gw-alice-remplace-moi", req); r.StatusCode != 200 {
		t.Fatalf("1re requête : %d", r.StatusCode)
	}
	if r := post(t, gw.URL+"/v1/chat/completions", "gw-alice-remplace-moi", req); r.StatusCode != 429 {
		t.Errorf("quota journalier : 429 attendu, obtenu %d", r.StatusCode)
	}
	g.C().Targets["mistral"].Quota = &Quota{RPD: 2} // quota partagé : alice en a déjà consommé 1
	r := post(t, gw.URL+"/v1/chat/completions", "gw-bob-remplace-moi", req)
	r2 := post(t, gw.URL+"/v1/chat/completions", "gw-bob-remplace-moi", req)
	if r.Header.Get("X-Gateway-Target") != "mistral/mistral-large" || r2.Header.Get("X-Gateway-Target") != "local/mistral-large" {
		t.Errorf("repli sur quota cible : %q puis %q", r.Header.Get("X-Gateway-Target"), r2.Header.Get("X-Gateway-Target"))
	}
}

func TestNameHeuristics(t *testing.T) {
	cfg, err := LoadConfig("config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := OpenStore(filepath.Join(t.TempDir(), "m.db"))
	learner, _ = NewLearner(st.db)
	defer func() { learner = nil }()
	anon := func(txt string) string {
		s := NewSession(cfg, cfg.Profiles["standard"])
		body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": txt}}}
		s.Process(body, "openai")
		return lastUserText(body)
	}
	for _, c := range []struct{ in, leak string }{
		{"Moniseur Gérald Keizer est très malade", "Keizer"},
		{"Gérald Keizer est très malade", "Keizer"},
		{"KEIZER Gérald a appelé", "KEIZER"},
		{"le patient Vandenbroucke est sorti", "Vandenbroucke"},
		{"Dhr. Janssens komt morgen", "Janssens"},
		{"Madmae Lefèvre et M. Keizr (typo de Keizer)", "Lefèvre"},
		{"Gérald Keizer puis Keizr dans la même phrase", "Keizr"},
	} {
		if out := anon(c.in); strings.Contains(out, c.leak) {
			t.Errorf("%q → %q (fuite de %q)", c.in, out, c.leak)
		}
	}
	time.Sleep(200 * time.Millisecond) // async learning
	for _, in := range []string{"Keizer a rappelé", "Kiezer a rappelé", "Vandenbrouke va mieux"} {
		if out := anon(in); !strings.Contains(out, "«PERSON_1»") {
			t.Errorf("mémoire : %q → %q", in, out)
		}
	}
	for _, in := range []string{"Paris est une belle ville.", "Monsieur le directeur, Bonjour.", "Le Conseil Européen se réunit.", "Microsoft Azure et Google Cloud",
		"Il me parle du Portugal et du planning.", "Avec Claude Code, le code est prêt.", "Claude Console aide.", "Le fichier maître Portugal est ici.", "CLIENT LIST (Initech)"} {
		if out := anon(in); out != in {
			t.Errorf("faux positif : %q → %q", in, out)
		}
	}
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM learned`).Scan(&n)
	rows, _ := st.db.Query(`SELECT vars FROM learned`)
	for rows.Next() {
		var b []byte
		rows.Scan(&b)
		if bytes.Contains(bytes.ToLower(b), []byte("keizer")) {
			t.Error("nom stocké en clair")
		}
	}
	if n == 0 {
		t.Error("aucun nom appris")
	}
}

func TestDowngradeJSONSchema(t *testing.T) {
	body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "x"}},
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "r", "schema": map[string]any{"type": "object"}}}}
	out := adaptBody(body, &Target{DowngradeJSONSchema: true}, "openai")
	if out["response_format"].(map[string]any)["type"] != "json_object" || len(out["messages"].([]any)) != 2 {
		t.Errorf("conversion : %v", out)
	}
	if body["response_format"].(map[string]any)["type"] != "json_schema" || len(body["messages"].([]any)) != 1 {
		t.Error("la requête d'origine ne doit pas être modifiée (repli sur une autre cible)")
	}
}

func TestDebugArchive(t *testing.T) {
	gw, _, _, g := setup(t)
	g.C().Debug.Enabled = true
	g.C().Profiles["standard"].Actions["NISS"] = "log"
	for _, stream := range []bool{false, true} {
		r := post(t, gw.URL+"/v1/chat/completions", "gw-alice-remplace-moi", map[string]any{
			"model": "mistral-large", "stream": stream, "messages": []any{map[string]any{"role": "user", "content": pii}}})
		io.ReadAll(r.Body)
	}
	time.Sleep(300 * time.Millisecond)
	rs, err := g.store.db.Query(`SELECT req, resp, mapping, events FROM debug`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rs.Next() {
		n++
		var req, resp, mp, ev string
		rs.Scan(&req, &resp, &mp, &ev)
		if strings.Contains(req, "claire.dubois@acme.be") || !strings.Contains(req, "«EMAIL_1»") {
			t.Errorf("requête archivée : doit être la vue anonymisée")
		}
		if !strings.Contains(resp, "«PERSON_") {
			t.Errorf("réponse archivée : doit être la réponse brute du LLM, avant restauration : %q", resp)
		}
		if !strings.Contains(mp, "claire.dubois@acme.be") || !strings.Contains(ev, `"Action":"log"`) {
			t.Errorf("détail : correspondances ou événements manquants\n%s\n%s", mp, ev)
		}
	}
	if n != 2 {
		t.Errorf("%d entrées archivées au lieu de 2", n)
	}
}

func TestContextDetectors(t *testing.T) {
	cfg, err := LoadConfig("config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	s := NewSession(cfg, cfg.Profiles["standard"])
	in := "Dossier patient 676888-67 , monsieur Jeremy Vaillant né le 30/12/2006"
	out := s.AnonymizeText(in)
	for _, leak := range []string{"676888-67", "Jeremy", "Vaillant", "30/12/2006"} {
		if strings.Contains(out, leak) {
			t.Errorf("fuite de %q : %s", leak, out)
		}
	}
	if s.Restore(out) != in {
		t.Errorf("restauration : %q", s.Restore(out))
	}
	for _, c := range []struct{ in, want string }{
		{"Réf. AB-2025/77 à traiter", "AB-2025/77"}, {"facture n° 2025-0142", "2025-0142"}, {"née le 3 mars 1985", "3 mars 1985"},
	} {
		s := NewSession(cfg, cfg.Profiles["standard"])
		if out := s.AnonymizeText(c.in); strings.Contains(out, c.want) {
			t.Errorf("%q non détecté : %s", c.want, out)
		}
	}
	for _, in := range []string{"Le dossier contient 3 pages.", "Réunion le 30/12/2026 à 10h.", "Le patient va mieux depuis 2019."} {
		s := NewSession(cfg, cfg.Profiles["standard"])
		if out := s.AnonymizeText(in); out != in {
			t.Errorf("faux positif : %q → %q", in, out)
		}
	}
}

func TestTechnicalFalsePositives(t *testing.T) {
	cfg, err := LoadConfig("config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := OpenStore(filepath.Join(t.TempDir(), "m.db"))
	learner, _ = NewLearner(st.db)
	defer func() { learner = nil }()
	cfg.Privacy.Allow = nil // the built-in list alone must be enough
	var keep []*Detector    // surname logic only: first names on their own are tested elsewhere
	for _, d := range cfg.dets {
		if d.builtin != "firstname_alone" {
			keep = append(keep, d)
		}
	}
	cfg.dets = keep
	anon := func(in string) string {
		s := NewSession(cfg, cfg.Profiles["standard"])
		body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": in}}}
		s.Process(body, "openai")
		return lastUserText(body)
	}
	// strong detections still work, including a common word after a title
	for _, c := range []struct{ in, leak string }{{"M. Transport a appelé", "Transport"}, {"Gérald Keizer arrive", "Keizer"}, {"KEIZER Gérald", "KEIZER"}} {
		if out := anon(c.in); strings.Contains(out, c.leak) {
			t.Errorf("%q → %q", c.in, out)
		}
	}
	for _, in := range []string{
		"Claude répond via l'API des providers.",
		"Claude",
		`{"id": 622, "status": 201, "count": 317, "patient_id": 617}`,
		"Transport: Custom provider",
		"Custom Transport for Claude Code",
		"API Claude et SDK Claude",
		"No 617 errors in file 317, case 201",
		"Voir le dossier 3 et la facture 12.",
		"Claude Transport Custom Provider",
		"Mistral Providers, Jean Settings et Marie Endpoints",
	} {
		if out := anon(in); out != in {
			t.Errorf("faux positif : %q → %q", in, out)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if n := learner.Count(); n != 0 { // M. Transport: dictionary word; Gérald Keizer: a guess from a first name
		t.Errorf("mémoire des noms : %d entrée(s), 0 attendue", n)
	}
}

// fakeSMTP: minimal SMTP server capturing messages.
// smtpMu guards the messages received by fakeSMTP (emails are sent asynchronously).
var smtpMu sync.Mutex

func fakeSMTP(t *testing.T) (string, int, *[]string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var msgs []string
	mu := &smtpMu
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				fmt.Fprint(c, "220 fake\r\n")
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch cmd := strings.ToUpper(strings.TrimSpace(l)); {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						fmt.Fprint(c, "250 fake\r\n")
					case cmd == "DATA":
						fmt.Fprint(c, "354 go\r\n")
						var b strings.Builder
						for {
							l, _ := r.ReadString('\n')
							if l == ".\r\n" {
								break
							}
							b.WriteString(l)
						}
						mu.Lock()
						msgs = append(msgs, b.String())
						mu.Unlock()
						fmt.Fprint(c, "250 ok\r\n")
					case cmd == "QUIT":
						fmt.Fprint(c, "221 bye\r\n")
						return
					default:
						fmt.Fprint(c, "250 ok\r\n")
					}
				}
			}(c)
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port, &msgs
}

func TestSystemPromptAndAlerts(t *testing.T) {
	gw, oai, _, g := setup(t)
	host, port, msgs := fakeSMTP(t)
	cfg := g.C()
	cfg.Profiles["strict"].SystemPrompt = "Réponds toujours en français."
	cfg.Profiles["strict"].AlertEmails = []string{"secu@example.test"}
	cfg.Alerts = AlertsCfg{Enabled: true, SMTPHost: host, SMTPPort: port, From: "llmgw@example.test", OnViolation: true, OnQuota: true, OnError: true}
	g.alerts = NewAlerter(g)
	post(t, gw.URL+"/v1/chat/completions", "gw-bob-remplace-moi", map[string]any{
		"model": "mistral-large", "messages": []any{map[string]any{"role": "user", "content": "Bonjour"}}})
	if !strings.Contains(oai.last(), "Réponds toujours en français.") {
		t.Error("prompt de comportement absent de la requête")
	}
	post(t, gw.URL+"/v1/chat/completions", "gw-bob-remplace-moi", map[string]any{ // NISS blocked for strict
		"model": "mistral-large", "messages": []any{map[string]any{"role": "user", "content": pii}}})
	g.alerts.flush()
	if len(*msgs) != 1 {
		t.Fatalf("%d email(s) au lieu de 1", len(*msgs))
	}
	m := (*msgs)[0]
	if !strings.Contains(m, "secu@example.test") || !strings.Contains(m, "NISS") || strings.Contains(m, "123.95") {
		t.Errorf("contenu de l'alerte :\n%s", m)
	}
}

func TestAcronymListAndSources(t *testing.T) {
	cfg, err := LoadConfig("config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := buildDetector(&DetectorCfg{Name: "COMPANY", Words: []string{"HUB", "CNDG", "cndg", "Globex"}}, ".", len(cfg.dets))
	if err != nil {
		t.Fatal(err)
	}
	cfg.dets = append(cfg.dets, d)
	s := NewSession(cfg, cfg.Profiles["standard"])
	s.Debug = true
	body := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "Clients : HUB, CNDG, globex."},
		map[string]any{"role": "user", "content": "Le hub réseau de cndg est en panne."},
	}}
	s.Process(body, "openai")
	out := lastUserText(body)
	if !strings.Contains(out, "hub réseau") || strings.Contains(out, "cndg") {
		t.Errorf("sigles : %q", out)
	}
	src := map[string]string{}
	for _, e := range s.Events {
		for k := range e.Sources {
			src[e.Value] += k + " "
		}
	}
	if !strings.Contains(src["HUB"], "system@2") || !strings.Contains(src["cndg"], "user@1") {
		t.Errorf("sources : %v", src)
	}
}

func TestPlacesPetsOrgsFirstnames(t *testing.T) {
	cfg, err := LoadConfig("config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	base := len(cfg.dets)
	for i, b := range []string{"address", "place", "pet", "firstname_alone", "be_health_orgs"} {
		d, err := buildDetector(&DetectorCfg{Name: strings.ToUpper(strings.ReplaceAll(b, "be_health_orgs", "org")), Builtin: b}, ".", base+i)
		if err != nil {
			t.Fatal(b, err)
		}
		cfg.dets = append(cfg.dets, d)
	}
	anon := func(in string) string { return NewSession(cfg, cfg.Profiles["standard"]).AnonymizeText(in) }
	for _, c := range []struct{ in, leak string }{
		{"Il habite rue de la Loi 16, 1000 Bruxelles.", "Loi 16"},
		{"Adresse : Kerkstraat 12, 9000 Gent", "Kerkstraat"},
		{"Site de Gosselies, puis Erasme et Bordet.", "Gosselies"},
		{"Site de Gosselies, puis Erasme et Bordet.", "Erasme"},
		{"Réunion au HUB et à l'H.U.B. avec le HUDERF.", "HUB"},
		{"Réunion au HUB et à l'H.U.B. avec le HUDERF.", "H.U.B"},
		{"Réunion au HUB et à l'H.U.B. avec le HUDERF.", "HUDERF"},
		{"Mon chien Rex et le chat nommé Félix.", "Rex"},
		{"Mon chien Rex et le chat nommé Félix.", "Félix"},
		{"Luc a rappelé, Els aussi.", "Luc"},
		{"Luc a rappelé, Els aussi.", "Els"},
		{"Ask Tom and Liz, then Jake.", "Liz"},
		{"domicilié à Montignies-sur-Sambre depuis 2019", "Montignies"},
	} {
		if out := anon(c.in); strings.Contains(out, c.leak) {
			t.Errorf("%q → %q (fuite de %q)", c.in, out, c.leak)
		}
	}
	if s := NewSession(cfg, cfg.Profiles["standard"]); strings.Count(s.AnonymizeText("Le HUB (H.U.B.) et le HUB"), "«COMPANY_1»") != 3 {
		t.Error("HUB et H.U.B. doivent partager le même jeton")
	}
	for _, in := range []string{
		"May I help you? Will you come in June?", "Rose est une couleur, Pierre est dure.", "Le hub central du réseau.",
		"En 2024 Microsoft a publié.", "The chat function and the dog-food build.", "Mark this as done, Grace period.",
	} {
		if out := anon(in); out != in {
			t.Errorf("faux positif : %q → %q", in, out)
		}
	}
}

func TestCountTokens(t *testing.T) {
	gw, _, ant, _ := setup(t)
	req := map[string]any{"model": "claude-sonnet-5", "messages": []any{map[string]any{"role": "user", "content": pii}}}
	r := post(t, gw.URL+"/v1/messages/count_tokens?beta=true", "gw-alice-remplace-moi", req)
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || !strings.Contains(string(b), "input_tokens") {
		t.Fatalf("status %d : %s", r.StatusCode, b)
	}
	noSecrets(t, ant.last())
	if r := post(t, gw.URL+"/v1/messages/count_tokens", "gw-bob-remplace-moi", req); r.StatusCode != 403 {
		t.Errorf("bob : 403 attendu (cible non autorisée ou NISS bloqué), obtenu %d", r.StatusCode)
	}
}

func TestModelAlias(t *testing.T) {
	cfg, _ := LoadConfig("config.example.toml")
	tg := cfg.Targets["anthropic"]
	tg.Models, tg.Aliases = []string{"deepseek-flash"}, map[string]string{"claude-*": "deepseek-flash", "claude-opus-*": "deepseek-v4-pro"}
	tg.pats = []*regexp.Regexp{regexp.MustCompile(`^deepseek-flash$`)}
	for in, want := range map[string]string{"claude-sonnet-4-6": "deepseek-flash", "claude-opus-5": "deepseek-v4-pro", "deepseek-flash": "deepseek-flash"} {
		cs := route(cfg, cfg.Profiles["standard"], "anthropic", in)
		if len(cs) == 0 || cs[0].model != want {
			t.Errorf("%s → %v, %s attendu", in, cs, want)
		}
	}
}

func TestOpenProfileLogsOnly(t *testing.T) {
	cfg, _ := LoadConfig("config.example.toml")
	open := &Profile{Targets: []string{"local"}, Images: "pass", DefaultAction: "log", Actions: map[string]string{}}
	s := NewSession(cfg, open)
	s.Debug = true
	if out := s.AnonymizeText(pii); out != pii {
		t.Errorf("OPEN ne doit rien modifier : %q", out)
	}
	if len(s.Events) == 0 || s.Stats["EMAIL"]["log"] == 0 || s.Anon != 0 || s.Blocked != 0 {
		t.Errorf("OPEN doit journaliser les détections : stats %v, conv %d", s.Stats, s.Anon)
	}
}

// fakeIdP: a minimal OpenID Connect provider signing RS256 ID tokens.
type fakeIdP struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	mu        sync.Mutex
	nonce     string
	challenge string
	claims    map[string]any
}

func newFakeIdP(t *testing.T) *fakeIdP {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeIdP{key: k}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		e := big.NewInt(int64(k.E)).Bytes()
		json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kid": "k1", "kty": "RSA",
			"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e)}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		h := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("code") != "code-ok" || base64.RawURLEncoding.EncodeToString(h[:]) != f.challenge {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		c := map[string]any{"iss": f.srv.URL, "aud": "client-1", "exp": time.Now().Add(time.Hour).Unix(), "nonce": f.nonce, "email_verified": true}
		for k, v := range f.claims {
			c[k] = v
		}
		hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1"})
		pl, _ := json.Marshal(c)
		in := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(pl)
		d := sha256.Sum256([]byte(in))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, d[:])
		json.NewEncoder(w).Encode(map[string]string{"id_token": in + "." + base64.RawURLEncoding.EncodeToString(sig)})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func TestSSO(t *testing.T) {
	_, oai, _, g := setup(t)
	idp := newFakeIdP(t)
	cfg := g.C()
	cfg.Auth = AuthCfg{BaseURL: "http://gw.test", Providers: []*ProviderCfg{{ID: "test", Type: "oidc", Issuer: idp.srv.URL,
		ClientID: "client-1", ClientSecret: "s", AdminRoles: []string{"LLMGW.Admin"},
		Mappings: []*AuthMapping{{Group: "g-sante", Profile: "strict"}, {Email: "*@ok.be", Profile: "standard"}}}}}
	cfg.Server.AdminToken = "tok"
	if err := cfg.checkAuth(); err != nil {
		t.Fatal(err)
	}
	g.sessKey = []byte("0123456789abcdef0123456789abcdef")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", g.handleLLM("openai"))
	mux.HandleFunc("GET /auth/login/{id}", g.handleAuthLogin)
	mux.HandleFunc("GET /auth/callback/{id}", g.handleAuthCallback)
	mux.HandleFunc("GET /auth/session", g.handleAuthSession)
	mux.HandleFunc("POST /auth/logout", g.handleAuthLogout)
	mux.HandleFunc("GET /admin/api/config", g.admin(g.handleGetConfig))
	gw := httptest.NewServer(mux)
	defer gw.Close()

	login := func(claims map[string]any) (*http.Client, int) {
		jar, _ := cookiejar.New(nil)
		cl := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		r, err := cl.Get(gw.URL + "/auth/login/test?next=/chat")
		if err != nil || r.StatusCode != 302 {
			t.Fatalf("login : %v %v", err, r)
		}
		u, _ := url.Parse(r.Header.Get("Location"))
		q := u.Query()
		if q.Get("redirect_uri") != "http://gw.test/auth/callback/test" || q.Get("code_challenge_method") != "S256" {
			t.Errorf("paramètres d'autorisation : %v", q)
		}
		idp.mu.Lock()
		idp.nonce, idp.challenge, idp.claims = q.Get("nonce"), q.Get("code_challenge"), claims
		idp.mu.Unlock()
		r, _ = cl.Get(gw.URL + "/auth/callback/test?code=code-ok&state=" + url.QueryEscape(q.Get("state")))
		return cl, r.StatusCode
	}
	call := func(cl *http.Client, path string, withHeader bool) int {
		var req *http.Request
		if strings.HasPrefix(path, "/v1/") {
			req, _ = http.NewRequest("POST", gw.URL+path, strings.NewReader(`{"model":"mistral-large","messages":[{"role":"user","content":"Bonjour"}]}`))
		} else {
			req, _ = http.NewRequest("GET", gw.URL+path, nil)
		}
		if withHeader {
			req.Header.Set(sessionHeader, "1")
		}
		r, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(r.Body)
		return r.StatusCode
	}

	// group -> profile "strict" (Mistral allowed), not admin
	cl, st := login(map[string]any{"email": "marie@hopital.be", "name": "Marie", "groups": []string{"g-sante"}})
	if st != 302 {
		t.Fatalf("callback : %d", st)
	}
	if c := call(cl, "/v1/chat/completions", true); c != 200 {
		t.Errorf("requête LLM avec session : %d", c)
	}
	if !strings.Contains(oai.last(), "Bonjour") {
		t.Error("la requête n'a pas atteint le LLM")
	}
	if c := call(cl, "/v1/chat/completions", false); c != 401 {
		t.Errorf("sans en-tête anti-CSRF : %d, 401 attendu", c)
	}
	if c := call(cl, "/admin/api/config", true); c != 401 {
		t.Errorf("dashboard pour un non-admin : %d, 401 attendu", c)
	}
	// admin role
	cl, _ = login(map[string]any{"email": "it@ok.be", "roles": []string{"LLMGW.Admin"}})
	if c := call(cl, "/admin/api/config", true); c != 200 {
		t.Errorf("dashboard pour un admin : %d", c)
	}
	// no mapping and no default profile: refused
	if _, st := login(map[string]any{"email": "x@ailleurs.com"}); st != 403 {
		t.Errorf("identité sans profil : %d, 403 attendu", st)
	}
	// declared user linked by email keeps its own profile
	cfg.Users["alice"].Email = "alice@hopital.be"
	cl, _ = login(map[string]any{"email": "ALICE@hopital.be"})
	req, _ := http.NewRequest("GET", gw.URL+"/auth/session", nil)
	r, _ := cl.Do(req)
	b, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(b), `"user":"alice"`) || !strings.Contains(string(b), `"profile":"standard"`) {
		t.Errorf("utilisateur lié par email : %s", b)
	}
	// unverified email: refused (anyone could sign up with an administrator's address)
	if _, st := login(map[string]any{"email": "it@ok.be", "email_verified": false, "roles": []string{"LLMGW.Admin"}}); st != 403 {
		t.Errorf("email non vérifié : %d, 403 attendu", st)
	}
	// rights follow the configuration: removing the admin rule applies to an open session
	cl, _ = login(map[string]any{"email": "it@ok.be", "roles": []string{"LLMGW.Admin"}})
	if c := call(cl, "/admin/api/config", true); c != 200 {
		t.Errorf("admin avant retrait : %d", c)
	}
	cfg.Auth.Providers[0].AdminRoles = nil
	if c := call(cl, "/admin/api/config", true); c != 401 {
		t.Errorf("admin après retrait de la règle : %d, 401 attendu", c)
	}
	cfg.Auth.Providers[0].AdminRoles = []string{"LLMGW.Admin"}
	// sign-out revokes the session: a copy of the cookie stops working
	gwURL, _ := url.Parse(gw.URL)
	stolen := cl.Jar.Cookies(gwURL)
	req, _ = http.NewRequest("POST", gw.URL+"/auth/logout", nil)
	cl.Do(req)
	jar0, _ := cookiejar.New(nil)
	jar0.SetCookies(gwURL, stolen)
	if c := call(&http.Client{Jar: jar0}, "/admin/api/config", true); c != 401 {
		t.Errorf("cookie copié après déconnexion : %d, 401 attendu", c)
	}
	// forged state
	jar, _ := cookiejar.New(nil)
	cl2 := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cl2.Get(gw.URL + "/auth/login/test")
	if r, _ := cl2.Get(gw.URL + "/auth/callback/test?code=code-ok&state=forged"); r.StatusCode != 400 {
		t.Errorf("state falsifié : %d, 400 attendu", r.StatusCode)
	}
}

func TestRolesAndAudit(t *testing.T) {
	_, _, _, g := setup(t)
	cfg := g.C()
	cfg.Server.AdminToken = "tok"
	cfg.Admins = []*AdminAccount{{Email: "boss@x.be", Role: "admin"}, {Email: "help@x.be", Role: "support"},
		{Email: "help2@x.be", Role: "support", Debug: true}, {Email: "gone@x.be", Role: "admin", Disabled: true}}
	g.sessKey = []byte("0123456789abcdef0123456789abcdef")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/api/config", g.staff(g.handleGetConfig))
	mux.HandleFunc("PUT /admin/api/config", g.staff(g.handlePutConfig))
	mux.HandleFunc("GET /admin/api/debug", g.guard(roleDebug, g.handleDebugList))
	mux.HandleFunc("GET /admin/api/audit", g.admin(g.handleAudit))
	mux.HandleFunc("POST /admin/api/test", g.admin(g.handleTest))
	gw := httptest.NewServer(mux)
	defer gw.Close()
	call := func(email, method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, gw.URL+path, strings.NewReader(body))
		if email != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: g.seal(webSession{User: email, Email: email, Exp: time.Now().Add(time.Hour).Unix()})})
			req.Header.Set(sessionHeader, "1")
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	// support: reads the configuration, saves users, but any other change is ignored
	st, body := call("help@x.be", "GET", "/admin/api/config", "")
	if st != 200 {
		t.Fatalf("support GET config : %d", st)
	}
	var got struct {
		Config  map[string]any
		Version string
	}
	json.Unmarshal([]byte(body), &got)
	got.Config["Users"].(map[string]any)["nouveau"] = map[string]any{"Key": "gw-nouveau-123456789", "Profile": "standard"}
	got.Config["Profiles"].(map[string]any)["standard"].(map[string]any)["DefaultAction"] = "off"
	pl, _ := json.Marshal(map[string]any{"Config": got.Config, "Version": got.Version})
	if st, b := call("help@x.be", "PUT", "/admin/api/config", string(pl)); st != 200 {
		t.Fatalf("support PUT config : %d %s", st, b)
	}
	if g.C().Users["nouveau"] == nil {
		t.Error("le support doit pouvoir créer un utilisateur")
	}
	if g.C().Profiles["standard"].DefaultAction != "anonymize" {
		t.Error("le support ne doit pas pouvoir modifier un profil")
	}
	// debug: refused to support, allowed to support with debug access and to admin
	if st, _ := call("help@x.be", "GET", "/admin/api/debug", ""); st != 403 {
		t.Errorf("support sans accès Debug : %d, 403 attendu", st)
	}
	if st, _ := call("help2@x.be", "GET", "/admin/api/debug", ""); st != 200 {
		t.Errorf("support avec accès Debug : %d", st)
	}
	if st, _ := call("help@x.be", "POST", "/admin/api/test", `{"Profile":"standard","Text":"x"}`); st != 403 {
		t.Errorf("support sur un écran admin : %d, 403 attendu", st)
	}
	if st, _ := call("gone@x.be", "GET", "/admin/api/config", ""); st != 401 {
		t.Errorf("compte désactivé : %d, 401 attendu", st)
	}
	if st, _ := call("intrus@x.be", "GET", "/admin/api/config", ""); st != 401 {
		t.Errorf("compte inconnu : %d, 401 attendu", st)
	}
	// audit: readable by admin only, records the support save
	if st, _ := call("help@x.be", "GET", "/admin/api/audit", ""); st != 403 {
		t.Errorf("journal d'audit pour le support : %d, 403 attendu", st)
	}
	st, body = call("boss@x.be", "GET", "/admin/api/audit", "")
	if st != 200 || !strings.Contains(body, "utilisateurs enregistrés") || !strings.Contains(body, "utilisateur nouveau créé") || !strings.Contains(body, "help@x.be") {
		t.Errorf("journal d'audit : %d %s", st, body)
	}
}

func TestBlockedFiles(t *testing.T) {
	gw, oai, _, g := setup(t)
	g.C().Profiles["standard"].Files = map[string]string{"pdf": "block", "zip": "block_alert", "jpg": "block"}
	send := func(content any) (int, string) {
		r := post(t, gw.URL+"/v1/chat/completions", "gw-alice-remplace-moi", map[string]any{"model": "mistral-large",
			"messages": []any{map[string]any{"role": "user", "content": content}}})
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	n0 := len(oai.bodies)
	if st, b := send([]any{map[string]any{"type": "text", "text": "résume"}, map[string]any{"type": "file", "file": map[string]any{"filename": "rapport.pdf", "file_data": "data:application/pdf;base64,AAAA"}}}); st != 403 || !strings.Contains(b, "rapport.pdf") {
		t.Errorf("PDF joint : %d %s", st, b)
	}
	if st, _ := send("Voici le fichier\n\n--- Document joint : archive.zip ---\ncontenu\n--- Fin du document ---"); st != 403 {
		t.Errorf("document zip du chat : %d", st)
	}
	if st, _ := send([]any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/jpeg;base64,AAAA"}}}); st != 403 {
		t.Errorf("image jpeg : %d", st)
	}
	if len(oai.bodies) != n0 {
		t.Error("un fichier interdit a atteint le LLM")
	}
	if st, _ := send("--- Document joint : note.docx ---\ntexte\n--- Fin du document ---"); st != 200 {
		t.Errorf("docx autorisé : %d", st)
	}
}

func TestFileActions(t *testing.T) {
	gw, oai, _, g := setup(t)
	p := g.C().Profiles["standard"]
	p.Files = map[string]string{"pdf": "blank", "docx": "blank", "csv": "pass_log", "exe": "block_alert"}
	p.FilesDefault, p.Images = "pass_log", "blank"
	send := func(content any) (int, string) {
		r := post(t, gw.URL+"/v1/chat/completions", "gw-alice-remplace-moi", map[string]any{"model": "mistral-large",
			"messages": []any{map[string]any{"role": "user", "content": content}}})
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	secret := base64.StdEncoding.EncodeToString([]byte("%PDF contrat confidentiel"))
	// blank: the PDF and the image become a white square, the content never reaches the LLM
	if st, b := send([]any{map[string]any{"type": "text", "text": "résume"},
		map[string]any{"type": "file", "file": map[string]any{"filename": "contrat.pdf", "file_data": "data:application/pdf;base64," + secret}},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}}}); st != 200 {
		t.Fatalf("blank : %d %s", st, b)
	}
	if last := oai.last(); strings.Contains(last, secret) || strings.Contains(last, "iVBORw0KGgo=\"") || strings.Count(last, whitePNG) != 2 {
		t.Errorf("le fichier ou l'image n'a pas été remplacé par le carré blanc : %.300s", last)
	}
	// chat document set to blank: text replaced, markers kept
	if st, _ := send("--- Document joint : note.docx ---\nJean Dupont, salaire 4000\n--- Fin du document ---"); st != 200 || strings.Contains(oai.last(), "4000") || !strings.Contains(oai.last(), blankDoc) {
		t.Errorf("document docx du chat non remplacé : %s", oai.last())
	}
	// pass_log: transmitted unchanged, recorded in the file log
	if st, _ := send("--- Document joint : export.csv ---\na;b\n--- Fin du document ---"); st != 200 || !strings.Contains(oai.last(), "a;b") {
		t.Errorf("csv non transmis")
	}
	// block_alert: refused
	if st, b := send([]any{map[string]any{"type": "file", "file": map[string]any{"filename": "setup.exe", "file_data": "data:application/octet-stream;base64,TVo="}}}); st != 403 || !strings.Contains(b, "setup.exe") {
		t.Errorf("exe : %d %s", st, b)
	}
	// the same csv sent again (conversation history) is not logged twice
	send("--- Document joint : export.csv ---\na;b\n--- Fin du document ---")
	time.Sleep(300 * time.Millisecond)
	got := map[string]string{}
	rows, _ := g.store.db.Query(`SELECT name, action FROM file_log`)
	n := 0
	for rows.Next() {
		var name, act string
		rows.Scan(&name, &act)
		got[name] = act
		n++
	}
	rows.Close()
	if got["export.csv"] != "pass_log" || got["setup.exe"] != "block_alert" || got["contrat.pdf"] != "" || n != 2 {
		t.Errorf("journal des fichiers : %v (%d lignes)", got, n)
	}
}

func TestFilesConfigMigration(t *testing.T) {
	c, err := ParseConfig([]byte(`[targets.local]
api = "openai"
base_url = "http://localhost:1/v1"
models = ["*"]
[profiles.p]
targets = ["local"]
images = "strip"
blocked_files = [".PDF", "zip"]
files = { zip = "block_alert" }
`), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := c.Profiles["p"]
	if p.Images != "blank" || p.Files["pdf"] != "block" || p.Files["zip"] != "block_alert" || p.FilesDefault != "pass_log" || p.BlockedFiles != nil {
		t.Errorf("migration : %+v", p)
	}
}

func TestSignInAudit(t *testing.T) {
	gw, _, _, g := setup(t)
	loginSeen.Range(func(k, _ any) bool { loginSeen.Delete(k); return true })
	g.C().Server.AdminToken = "tok"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/me", g.handleMe)
	mux.HandleFunc("GET /admin/api/me", g.staff(g.handleWhoami))
	web := httptest.NewServer(mux)
	defer web.Close()
	get := func(path string, h map[string]string) int {
		req, _ := http.NewRequest("GET", web.URL+path, nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	alice := map[string]string{"Authorization": "Bearer gw-alice-remplace-moi"}
	get("/v1/me", alice)
	get("/v1/me", alice) // same sign-in: recorded once
	get("/v1/me", map[string]string{"Authorization": "Bearer gw-faux"})
	get("/admin/api/me", map[string]string{"X-Admin-Token": "tok"})
	get("/admin/api/me", map[string]string{"X-Admin-Token": "mauvais"})
	// an application (no browser headers) and a call from the chat page (browser: not recorded)
	post(t, gw.URL+"/v1/chat/completions", "gw-alice-remplace-moi", map[string]any{"model": "mistral-large", "messages": []any{map[string]any{"role": "user", "content": "bonjour"}}}).Body.Close()

	rows, err := g.store.db.Query(`SELECT actor, action, target FROM audit ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var a, b, c string
		rows.Scan(&a, &b, &c)
		got = append(got, a+"|"+b+"|"+c)
	}
	want := []string{"alice|connexion|Chat (clé)", "inconnu|connexion refusée|Chat (clé)", "jeton d'administration|connexion|Admin (jeton)",
		"inconnu|connexion refusée|Admin (jeton)", "alice|connexion|Application (clé)"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("journal des connexions :\n%s\nattendu :\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestGarbledPDFName(t *testing.T) {
	cfg, err := LoadConfig("config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	s := NewSession(cfg, cfg.Profiles["standard"])
	in := "Comparaît :\n—   Monsiaur   OPDEBEECK   J@an—S©basti@:,   ici   présent, et **Marie Dupont** (marie.dupont@exemple.be) – Gérald Keizer—"
	out := s.AnonymizeText(in)
	for _, leak := range []string{"OPDEBEECK", "J@an", "S©basti", "Dupont", "marie.dupont@exemple.be", "Keizer"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q non masqué : %s", leak, out)
		}
	}
	if back := s.Restore(out); back != in {
		t.Errorf("restauration :\n%s\nattendu :\n%s", back, in)
	}
}

func TestINAMI(t *testing.T) {
	// 123456 mod 97 = 72 → key 25 ; 123456 mod 89 = 13 → key 76 (newer series)
	cases := map[string]bool{
		"Dr Martin, n° INAMI 1-23456-25-001, consulte le lundi.": true,
		"prescripteur 12345625001":                               true,
		"RIZIV 1.23456.76.000":                                   true,
		"INAMI 1-23456-25":                                       true,  // 8 digits: after the context word only
		"code 1-23456-25":                                        false, // 8 digits without context
		"numéro 12345626001":                                     false, // wrong key
		"total 1 234 567 890 1":                                  false,
	}
	for s, want := range cases {
		if got := len(findINAMI(s)) > 0; got != want {
			t.Errorf("findINAMI(%q) = %v, attendu %v", s, got, want)
		}
	}
	gw, oai, _, _ := setup(t)
	r := post(t, gw.URL+"/v1/chat/completions", "gw-alice-remplace-moi", map[string]any{"model": "mistral-large",
		"messages": []any{map[string]any{"role": "user", "content": "Le Dr Peeters (INAMI 1-23456-25-001) a prescrit le traitement."}}})
	io.ReadAll(r.Body)
	if last := oai.last(); strings.Contains(last, "23456") || !strings.Contains(last, "«INAMI_1»") {
		t.Errorf("numéro INAMI non anonymisé : %s", last)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{"/chat": "/chat", "/dashboard": "/dashboard", "/dashboard#users": "/dashboard#users",
		"/\t/evil.com": "/chat", "//evil.com": "/chat", "/\\evil.com": "/chat", "https://evil.com": "/chat", "/chat/../admin": "/chat",
		"/%09/evil.com": "/chat", "": "/chat", "/admin/api/config": "/chat"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, %q attendu", in, got, want)
		}
	}
}

func TestShield(t *testing.T) {
	_, _, _, g := setup(t)
	cfg := g.C()
	cfg.Security = SecurityCfg{BanAfter: 3, RatePerMin: 20, AdminAllow: []string{"10.0.0.0/8"}}
	if err := cfg.Security.check("."); err != nil {
		t.Fatal(err)
	}
	g.shield = NewShield()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", g.handleLLM("openai"))
	mux.HandleFunc("GET /admin/api/config", g.admin(g.handleGetConfig))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(clientIP(r))) })
	gw := httptest.NewServer(g.protect(mux))
	defer gw.Close()
	do := func(method, path, ip, key string) *http.Response {
		req, _ := http.NewRequest(method, gw.URL+path, strings.NewReader(`{"model":"mistral-large","messages":[{"role":"user","content":"x"}]}`))
		req.Header.Set("X-Forwarded-For", "6.6.6.6, "+ip) // the left part is written by the client: ignored
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(r.Body)
		r.Body.Close()
		return r
	}
	r := do("GET", "/healthz", "203.0.113.9", "")
	if r.Header.Get("X-Frame-Options") != "DENY" || r.Header.Get("Content-Security-Policy") == "" {
		t.Error("en-têtes de sécurité absents")
	}
	req, _ := http.NewRequest("GET", gw.URL+"/healthz", nil)
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	rr, _ := http.DefaultClient.Do(req)
	if b, _ := io.ReadAll(rr.Body); string(b) != "203.0.113.9" {
		t.Errorf("adresse du client derrière le proxy : %q", b)
	}
	// admin allow list
	if r := do("GET", "/admin/api/config", "203.0.113.9", ""); r.StatusCode != 403 {
		t.Errorf("administration hors liste : %d, 403 attendu", r.StatusCode)
	}
	// three bad keys: banned, even with a good key afterwards
	for i := 0; i < 3; i++ {
		if r := do("POST", "/v1/chat/completions", "198.51.100.7", "gw-mauvaise"); r.StatusCode != 401 {
			t.Fatalf("mauvaise clé : %d", r.StatusCode)
		}
	}
	if r := do("POST", "/v1/chat/completions", "198.51.100.7", "gw-alice-remplace-moi"); r.StatusCode != 403 {
		t.Errorf("adresse bannie : %d, 403 attendu", r.StatusCode)
	}
	if r := do("POST", "/v1/chat/completions", "198.51.100.8", "gw-alice-remplace-moi"); r.StatusCode != 200 {
		t.Errorf("autre adresse : %d, 200 attendu", r.StatusCode)
	}
	if b := g.shield.Bans(); len(b) != 1 || b[0].Address != "198.51.100.7" {
		t.Errorf("liste des blocages : %+v", b)
	}
	if !g.shield.Unban("198.51.100.7") || do("POST", "/v1/chat/completions", "198.51.100.7", "gw-alice-remplace-moi").StatusCode != 200 {
		t.Error("déblocage sans effet")
	}
	// rate limit: 20 per minute
	n429 := 0
	for i := 0; i < 25; i++ {
		if do("GET", "/healthz", "192.0.2.1", "").StatusCode == 429 {
			n429++
		}
	}
	if n429 != 5 {
		t.Errorf("limite de débit : %d refus, 5 attendus", n429)
	}
	// IPv6: one bucket per /64
	if bucket("2001:db8:1:2:aaaa::1") != bucket("2001:db8:1:2:bbbb::9") || bucket("2001:db8:1:3::1") == bucket("2001:db8:1:2::1") {
		t.Error("regroupement IPv6 par /64")
	}
}

func TestSecretBinding(t *testing.T) {
	_, _, _, g := setup(t)
	cur := g.C()
	cur.Server.AdminToken = "tok"
	cur.Targets["openai"].APIKeyEnv = "OPENAI_API_KEY"
	put := func(mod func(c *Config)) int {
		c := cloneConfig(cur)
		for _, t := range c.Targets {
			if t.APIKey != "" {
				t.APIKey = keepSecret
			}
		}
		for _, u := range c.Users {
			u.Key = keepSecret
		}
		mod(c)
		dir := t.TempDir()
		p := filepath.Join(dir, "config.toml")
		os.WriteFile(p, nil, 0o600)
		cur.path = p
		b, _ := json.Marshal(map[string]any{"Config": c})
		req := httptest.NewRequest("PUT", "/admin/api/config", bytes.NewReader(b))
		req.Header.Set("X-Admin-Token", "tok")
		w := httptest.NewRecorder()
		g.handlePutConfig(w, req)
		return w.Code
	}
	if c := put(func(c *Config) { c.Targets["openai"].BaseURL = "https://evil.example/v1" }); c != 400 {
		t.Errorf("clé d'environnement envoyée vers une nouvelle adresse : %d, 400 attendu", c)
	}
	if c := put(func(c *Config) { c.Targets["openai"].Description = "modifiée" }); c != 200 {
		t.Errorf("modification sans changement d'adresse : %d", c)
	}
	g.cfg.Store(cur)
	cur.Targets["mistral"].APIKey = "sk-secret"
	if c := put(func(c *Config) { c.Targets["mistral"].BaseURL = "https://evil.example/v1" }); c != 400 {
		t.Errorf("clé conservée vers une nouvelle adresse : %d, 400 attendu", c)
	}
	g.cfg.Store(cur)
	if c := put(func(c *Config) {
		c.Targets["evil"] = &Target{API: "openai", BaseURL: "https://evil.example/v1", APIKeyEnv: "OPENAI_API_KEY"}
	}); c != 400 {
		t.Errorf("variable d'une autre cible envoyée ailleurs : %d, 400 attendu", c)
	}
}

func TestAccessRequests(t *testing.T) {
	_, _, _, g := setup(t)
	host, port, msgs := fakeSMTP(t)
	cfg := g.C()
	cfg.Server.AdminToken = "tok"
	cfg.Alerts = AlertsCfg{SMTPHost: host, SMTPPort: port, From: "llmgw@example.test", To: []string{"rssi@example.test"}}
	cfg.Access = AccessCfg{Enabled: true, TrustedDomains: []string{"hopital.be"}, DefaultProfile: "standard"}
	cfg.Auth.BaseURL = "https://gw.test"
	if err := cfg.checkAccess(); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /access/request", g.handleAccessRequest)
	mux.HandleFunc("POST /access/verify", g.handleAccessVerify)
	mux.HandleFunc("POST /access/key", g.handleAccessKey)
	mux.HandleFunc("GET /admin/api/access", g.staff(g.handleAccessList))
	mux.HandleFunc("POST /admin/api/access/{id}/{act}", g.staff(g.handleAccessDecide))
	gw := httptest.NewServer(mux)
	defer gw.Close()
	got := func() []string { smtpMu.Lock(); defer smtpMu.Unlock(); return append([]string{}, *msgs...) }
	mail := func(n int) string { // the n-th email, sent asynchronously
		for i := 0; i < 100 && len(got()) < n; i++ {
			time.Sleep(20 * time.Millisecond)
		}
		m := got()
		if len(m) < n {
			t.Fatalf("%d email(s), %d attendu(s)", len(m), n)
		}
		return m[n-1]
	}
	link := regexp.MustCompile(`https://gw\.test(/access/\w+)\?t=([\w-]+)`)
	request := func(email string) {
		b, _ := json.Marshal(map[string]string{"Name": "Marie Test", "Email": email, "Reason": "projet", "Lang": "fr"})
		r, err := http.Post(gw.URL+"/access/request", "application/json", bytes.NewReader(b))
		if err != nil || r.StatusCode != 200 {
			t.Fatalf("demande : %v %v", err, r)
		}
	}
	form := func(path, tok string) (int, string) {
		r, err := http.PostForm(gw.URL+path, url.Values{"t": {tok}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	keyRe := regexp.MustCompile(`value="(gw-[0-9a-f]+)"`)

	// trusted domain: confirmed link → approved at once, key shown once
	request("marie@hopital.be")
	m := link.FindStringSubmatch(mail(1))
	if m == nil || m[1] != "/access/verify" {
		t.Fatalf("lien de confirmation absent :\n%s", mail(1))
	}
	st, page := form("/access/verify", m[2])
	k := keyRe.FindStringSubmatch(page)
	if st != 200 || k == nil {
		t.Fatalf("confirmation (domaine de confiance) : %d\n%s", st, page)
	}
	if u := g.C().Users["marie"]; u == nil || u.Key != k[1] || u.Profile != "standard" || u.Email != "marie@hopital.be" {
		t.Errorf("utilisateur créé : %+v", u)
	}
	if st, _ := form("/access/verify", m[2]); st != 400 {
		t.Errorf("lien réutilisé : %d, 400 attendu", st)
	}
	if strings.Contains(strings.Join(got(), ""), k[1]) {
		t.Error("la clé ne doit jamais partir par email")
	}

	// other domain: waits for a decision, then a link to the key
	request("paul@ailleurs.com")
	m = link.FindStringSubmatch(mail(2))
	if st, page := form("/access/verify", m[2]); st != 200 || keyRe.MatchString(page) {
		t.Fatalf("confirmation (externe) : %d\n%s", st, page)
	}
	if !strings.Contains(mail(3), "rssi@example.test") {
		t.Error("administrateurs non prévenus")
	}
	if g.C().Users["paul"] != nil {
		t.Fatal("un domaine externe ne doit pas être approuvé automatiquement")
	}
	admin := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, gw.URL+path, strings.NewReader(body))
		req.Header.Set("X-Admin-Token", "tok")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	_, list := admin("GET", "/admin/api/access", "")
	var l struct {
		Requests []accessReq
		Pending  int
	}
	json.Unmarshal([]byte(list), &l)
	if l.Pending != 1 || l.Requests[0].Email != "paul@ailleurs.com" || l.Requests[0].Status != "pending" {
		t.Fatalf("liste des demandes : %s", list)
	}
	if st, b := admin("POST", fmt.Sprintf("/admin/api/access/%d/approve", l.Requests[0].ID), `{"Profile":"strict"}`); st != 200 {
		t.Fatalf("approbation : %d %s", st, b)
	}
	if u := g.C().Users["paul"]; u == nil || u.Profile != "strict" {
		t.Fatalf("utilisateur approuvé : %+v", u)
	}
	m = link.FindStringSubmatch(mail(4))
	if m == nil || m[1] != "/access/key" {
		t.Fatalf("lien vers la clé absent :\n%s", mail(4))
	}
	st, page = form("/access/key", m[2])
	if k := keyRe.FindStringSubmatch(page); st != 200 || k == nil || k[1] != g.C().Users["paul"].Key {
		t.Errorf("affichage de la clé : %d\n%s", st, page)
	}
	if st, _ := form("/access/key", m[2]); st != 400 {
		t.Errorf("clé affichée deux fois : %d, 400 attendu", st)
	}
	// unknown link
	if st, _ := form("/access/key", "inventé-0123456789abcdefghij"); st != 400 {
		t.Errorf("lien inventé : %d", st)
	}
}
