package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
