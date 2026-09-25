package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	if strings.Contains(up, `"image"`) {
		t.Error("image non retirée")
	}
	var got string
	for _, l := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(l, "data: ") && strings.Contains(l, "text_delta") {
			var m map[string]any
			json.Unmarshal([]byte(l[6:]), &m)
			got += m["delta"].(map[string]any)["text"].(string)
		}
	}
	want := pii + "[image retiré(e) par le gateway]"
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
	for _, rng := range []string{"48h", "60d", "6w", "12m", "3y"} {
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
func fakeSMTP(t *testing.T) (string, int, *[]string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var msgs []string
	var mu sync.Mutex
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
