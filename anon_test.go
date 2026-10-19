package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	mrand "math/rand"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"
)

// Regression tests of the anonymization engine (review of October 2026) and of its fast paths.

func anonConfig(t testing.TB) *Config {
	t.Helper()
	cfg, err := LoadConfig("config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// anonProcess runs Process on a request given as JSON and returns it as sent to the LLM.
func anonProcess(t *testing.T, cfg *Config, prof *Profile, api, js string) (map[string]any, string, *Session) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(js), &body); err != nil {
		t.Fatal(err)
	}
	s := NewSession(cfg, prof)
	s.Process(body, api)
	return body, string(encodeJSON(body)), s
}

func noLeak(t *testing.T, out string, secrets ...string) {
	t.Helper()
	for _, x := range secrets {
		if strings.Contains(out, x) {
			t.Errorf("fuite de %q : %s", x, out)
		}
	}
}

func lastMessage(body map[string]any) map[string]any {
	ms := body["messages"].([]any)
	return ms[len(ms)-1].(map[string]any)
}

func withActions(p *Profile, kv ...string) *Profile {
	c := *p
	c.Actions = map[string]string{}
	for k, v := range p.Actions {
		c.Actions[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		c.Actions[kv[i]] = kv[i+1]
	}
	return &c
}

// 1. JSON text inside a string (tool results, tool arguments) is scanned unescaped and stays valid.
func TestJSONTextInStrings(t *testing.T) {
	cfg := anonConfig(t)
	std := cfg.Profiles["standard"]

	// OpenAI tool call arguments, as produced by json.dumps
	args := `{"body":"Bonjour,\nClaire Dubois a appel\u00e9","to":"x\nclaire@acme.be"}`
	js, _ := json.Marshal(map[string]any{"model": "m", "messages": []any{map[string]any{"role": "assistant", "content": nil,
		"tool_calls": []any{map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "send", "arguments": args}}}}}})
	body, _, _ := anonProcess(t, cfg, std, "openai", string(js))
	got := lastMessage(body)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
	noLeak(t, got, "Dubois", "claire@acme.be")
	var a map[string]string
	if err := json.Unmarshal([]byte(got), &a); err != nil || a["to"] != "x\n«EMAIL_1»" || !strings.HasPrefix(a["body"], "Bonjour,\n«PERSON_") {
		t.Errorf("arguments : %q (%v)", got, err)
	}

	for _, c := range []struct {
		api, content string
		secrets      []string
	}{
		{"openai", `{"nom": "G\u00e9rald Keizer", "note": "\nMme Lef\u00e8vre"}`, []string{"Keizer", "Lef\\u00e8vre", "Lefèvre"}},
		{"anthropic", `{"note":"Rappel:\nClaire Vandamme a appelé","tel":"+32 470 12 34 56"}`, []string{"Vandamme", "470 12"}},
		{"anthropic", `{"raw":"{\"note\":\"Rappel:\\nClaire Vandamme\",\"nom\":\"Lef\\u00e8vre\"}"}`, []string{"Vandamme"}}, // JSON in JSON
		{"openai", `{"niss": 90010112395, "n": 3}`, []string{"90010112395"}},
		{"openai", `{'note': 'Rappel:\nClaire Vandamme'}`, []string{"Vandamme"}}, // Python repr: not JSON, escapes decoded all the same
	} {
		var msg map[string]any
		if c.api == "openai" {
			msg = map[string]any{"role": "tool", "tool_call_id": "c1", "content": c.content}
		} else {
			msg = map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": c.content}}}
		}
		js, _ := json.Marshal(map[string]any{"model": "m", "messages": []any{msg}})
		body, _, s := anonProcess(t, cfg, std, c.api, string(js))
		m := lastMessage(body)
		out, _ := m["content"].(string)
		if c.api == "anthropic" {
			out = m["content"].([]any)[0].(map[string]any)["content"].(string)
		}
		noLeak(t, out, c.secrets...)
		if jsonDoc(c.content) && !json.Valid([]byte(out)) {
			t.Errorf("JSON invalide : %s", out)
		}
		if strings.Contains(c.content, "niss") {
			var v map[string]any
			json.Unmarshal([]byte(out), &v)
			if v["niss"] != "«NISS_1»" {
				t.Errorf("nombre JSON : %s", out)
			}
		}
		if strings.Contains(c.content, "Keizer") && s.rev["PERSON_1"] != "Gérald Keizer" {
			t.Errorf("valeur restaurée : %q, la forme décodée est attendue", s.rev["PERSON_1"])
		}
		if strings.HasPrefix(c.content, `{"raw"`) {
			var v map[string]string
			json.Unmarshal([]byte(out), &v)
			if !json.Valid([]byte(v["raw"])) {
				t.Errorf("JSON imbriqué invalide : %s", v["raw"])
			}
		}
	}
}

// 2. request parts that were never walked: OpenAI user, name and stop, Anthropic metadata, tools,
// documents and stop sequences; signatures and base64 stay untouched.
func TestRequestPartsWalked(t *testing.T) {
	cfg := anonConfig(t)
	std := cfg.Profiles["standard"]
	body, out, _ := anonProcess(t, cfg, std, "openai", `{"model":"m","user":"claire.dubois@acme.be","stop":["Claire Dubois"],
		"messages":[{"role":"user","name":"Claire_Dubois","content":"ok"},{"role":"tool","name":"get_Claire","tool_call_id":"c","content":"x"}]}`)
	noLeak(t, out, "claire.dubois@acme.be", "Dubois")
	if n := body["messages"].([]any)[1].(map[string]any)["name"].(string); !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(n) {
		t.Errorf("name hors du motif OpenAI : %q", n)
	}
	if n := lastMessage(body)["name"]; n != "get_Claire" {
		t.Errorf("nom de fonction modifié : %v", n)
	}

	sig := "Claire Dubois 0470 12 34 56 sig"
	pdf := base64.StdEncoding.EncodeToString([]byte("%PDF Claire Dubois 0470 12 34 56"))
	js, _ := json.Marshal(map[string]any{"model": "m", "metadata": map[string]any{"user_id": "claire.dubois@acme.be"},
		"stop_sequences": []any{"Claire Dubois"},
		"tools": []any{map[string]any{"name": "crm_Dubois", "description": "Contacte toujours claire.dubois@acme.be",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{
				"to": map[string]any{"type": "string", "description": "par défaut Mme Claire Dubois", "enum": []any{"claire.dubois@acme.be"}}}}}},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "document", "source": map[string]any{"type": "text", "media_type": "text/plain", "data": "Patient Mme Claire Dubois, NISS 90.01.01-123.95"}},
				map[string]any{"type": "document", "source": map[string]any{"type": "content", "content": []any{map[string]any{"type": "text", "text": "Mme Claire Dubois claire.dubois@acme.be"}}}},
				map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": pdf}},
				map[string]any{"type": "text", "text": "Résume"}}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "Claire Dubois", "signature": sig},
				map[string]any{"type": "redacted_thinking", "data": sig}}}}})
	body, _, _ = anonProcess(t, cfg, std, "anthropic", string(js))
	tool := body["tools"].([]any)[0].(map[string]any)
	if tool["name"] != "crm_Dubois" {
		t.Errorf("nom d'outil modifié : %v", tool["name"])
	}
	if _, ok := tool["input_schema"].(map[string]any)["properties"].(map[string]any)["to"]; !ok {
		t.Error("nom de propriété modifié")
	}
	tool["name"] = ""
	msgs := body["messages"].([]any)
	asst := msgs[1].(map[string]any)["content"].([]any)
	if asst[0].(map[string]any)["signature"] != sig || asst[0].(map[string]any)["thinking"] != "Claire Dubois" || asst[1].(map[string]any)["data"] != sig {
		t.Error("bloc signé modifié")
	}
	if d := msgs[0].(map[string]any)["content"].([]any)[2].(map[string]any)["source"].(map[string]any)["data"]; d != pdf {
		t.Error("document base64 modifié")
	}
	msgs[1] = nil
	msgs[0].(map[string]any)["content"].([]any)[2] = nil
	noLeak(t, string(encodeJSON(body)), "claire.dubois@acme.be", "Dubois", "123.95")
}

// 3. Unicode spaces and format characters (Word, PDF, chat apps) inside the values.
func TestUnicodeFormatChars(t *testing.T) {
	cfg := anonConfig(t)
	for _, c := range []struct{ in, gone string }{
		{"Tel 0470\u00a012\u00a034\u00a056 svp", "0470"},
		{"Tel 0470\u202f12\u202f34\u202f56 svp", "0470"},
		{"Tel 0470\u200912\u200934\u200956 svp", "0470"},
		{"IBAN BE68\u00a05390\u00a00754\u00a07034 svp", "5390"},
		{"IBAN be68 5390 0754 7034 svp", "5390"},
		{"IBAN BE68-5390-0754-7034 svp", "5390"},
		{"IBAN BE68.5390.0754.7034 svp", "5390"},
		{"NISS 90.01.01\u00a0123.95 svp", "123"},
		{"NISS 90.01.01 123.95 svp", "123"},
		{"NISS 900101/123-95 svp", "123"},
		{"carte 4111\u00a01111\u00a01111\u00a01111", "1111"},
		{"Mme Du\u00adbois a appelé", "bois"},
		{"Mme Du\u200bbois a appelé", "bois"},
		{"Mme Du\u200dbo\ufeffis a appelé", "bo"},
		{"mail claire.dubois@acme\u200b.be svp", "acme"},
		{"mail jérôme.dupont@acme.be svp", "jérô"},
	} {
		s := NewSession(cfg, cfg.Profiles["standard"])
		out := s.AnonymizeText(c.in)
		if strings.Contains(out, c.gone) || !strings.Contains(out, "«") {
			t.Errorf("%q → %q", c.in, out)
		}
		if back := s.Restore(out); back != c.in {
			t.Errorf("restauration : %q, attendu %q", back, c.in)
		}
	}
}

// 4. a name is not allowed because its first word is ("Claude", "Hermes").
func TestAllowListNames(t *testing.T) {
	cfg := anonConfig(t)
	for _, c := range []struct{ in, gone string }{
		{"Mme Claude Dubois est hospitalisée.", "Dubois"},
		{"Le patient Hermes Vandamme est sorti.", "Vandamme"},
	} {
		if out := NewSession(cfg, cfg.Profiles["standard"]).AnonymizeText(c.in); strings.Contains(out, c.gone) {
			t.Errorf("%q → %q", c.in, out)
		}
	}
	for _, in := range []string{"Avec Claude Code, le code est prêt.", "Le client Claude Cowork est installé.", "Mme Claude Code ?", "Claude Console aide."} {
		if out := NewSession(cfg, cfg.Profiles["standard"]).AnonymizeText(in); out != in {
			t.Errorf("faux positif : %q → %q", in, out)
		}
	}
}

// 5. on overlapping detections the strictest action wins.
func TestStrictestActionWins(t *testing.T) {
	cfg := anonConfig(t)
	strict := cfg.Profiles["strict"]
	for _, p := range []*Profile{withActions(strict, "REF", "log"), strict} {
		s := NewSession(cfg, p)
		s.AnonymizeText("réf. 90010112395/A")
		if s.Blocked == 0 || !strings.Contains(s.BlockedKinds(), "NISS") {
			t.Errorf("REF=%s : NISS non bloqué (%v)", p.Actions["REF"], s.Stats)
		}
	}
	s := NewSession(cfg, withActions(cfg.Profiles["standard"], "REF", "log"))
	if out := s.AnonymizeText("réf. 90010112395/A"); strings.Contains(out, "90010112395") {
		t.Errorf("NISS caché par une référence journalisée : %q", out)
	}
}

// 6. values restored in tool arguments are JSON-escaped (stream and non-stream).
func TestRestoreJSONSafe(t *testing.T) {
	cfg := anonConfig(t)
	s := NewSession(cfg, cfg.Profiles["standard"])
	v1, v2 := "Claire\tDubois", "Jean \"Le Grand\" \\ Dupont\n"
	s.rev = map[string]string{"PERSON_1": v1, "PERSON_2": v2}
	frag := `{"a":"«PERSON_1»","b":"«PERSON_2»"}`
	check := func(what, got string) {
		t.Helper()
		var m map[string]string
		if err := json.Unmarshal([]byte(got), &m); err != nil || m["a"] != v1 || m["b"] != v2 {
			t.Errorf("%s : %q (%v)", what, got, err)
		}
	}
	for _, f := range []string{"partial_json", "arguments"} {
		for _, n := range []int{1, 3, 7} {
			r := newRestorer(s)
			var got string
			for _, c := range chunks(frag, n) {
				got += r.Push(rkey{0, 1, f}, c)
			}
			check(fmt.Sprintf("flux %s/%d", f, n), got+r.Flush(rkey{0, 1, f}))
		}
	}
	r := newRestorer(s)
	if got := r.Push(rkey{0, -1, "text"}, "«PERSON_1»") + r.Flush(rkey{0, -1, "text"}); got != v1 {
		t.Errorf("texte : %q", got)
	}
	resp := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "«PERSON_2»",
		"tool_calls": []any{map[string]any{"function": map[string]any{"arguments": frag}}}}}}}
	restoreAll(resp, s.Restore)
	msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	check("non-stream", msg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(string))
	if msg["content"] != v2 {
		t.Errorf("contenu : %q", msg["content"])
	}
	tu := map[string]any{"content": []any{map[string]any{"type": "tool_use", "input": map[string]any{"a": "«PERSON_1»"}}}}
	restoreAll(tu, s.Restore)
	if got := tu["content"].([]any)[0].(map[string]any)["input"].(map[string]any)["a"]; got != v1 {
		t.Errorf("tool_use : %q", got)
	}
}

// 7. a learned name seen again with other spaces gets the same (restorable) token.
func TestLearnedWhitespace(t *testing.T) {
	cfg := anonConfig(t)
	for _, in := range []string{"Mme Sophie Vandamme a écrit.\nSignature : Sophie\nVandamme", "Mme Claire Dubois a écrit. Claire    Dubois"} {
		s := NewSession(cfg, cfg.Profiles["standard"])
		out := s.AnonymizeText(in)
		if strings.Contains(out, "«_") || strings.Count(out, "«PERSON_1»") != 2 {
			t.Errorf("%q → %q", in, out)
		}
		for _, m := range phRe.FindAllStringSubmatch(out, -1) {
			if _, ok := s.rev[m[1]]; !ok {
				t.Errorf("jeton non restaurable %s dans %q", m[0], out)
			}
		}
	}
}

// 8. a name does not run into the next sentence; learned words lose their punctuation.
func TestNameStopsAtSentenceEnd(t *testing.T) {
	cfg := anonConfig(t)
	for _, c := range []struct{ in, keep, gone string }{
		{"Le patient Dupont. Demain il sort.", "Demain il sort.", "Dupont"},
		{"Dr. Peeters et M. Keizer", " et ", "Peeters"},
		{"Mme Claire Morel a appelé. Model 3 : voir Model.fit() et la classe Model.", "Model 3 : voir Model.fit() et la classe Model.", "Morel"},
	} {
		s := NewSession(cfg, cfg.Profiles["standard"])
		out := s.AnonymizeText(c.in)
		if !strings.Contains(out, c.keep) || strings.Contains(out, c.gone) {
			t.Errorf("%q → %q", c.in, out)
		}
		for k := range s.learn {
			if strings.ContainsAny(k, ".,;:") {
				t.Errorf("valeur apprise avec sa ponctuation : %q", k)
			}
		}
	}
}

// 9. typos of a learned name: not another name, not a common word; the restored form keeps its case.
func TestFuzzyNames(t *testing.T) {
	cfg := anonConfig(t)
	for _, in := range []string{"Mme Claire Dupont puis Dumont a rappelé.", "Mme Anne Wouters a appelé. Les Routers sont en panne."} {
		s := NewSession(cfg, cfg.Profiles["standard"])
		if out := s.AnonymizeText(in); s.Restore(out) != in || strings.Contains(in, "Routers") && !strings.Contains(out, "Routers") {
			t.Errorf("%q → %q → %q", in, out, s.Restore(out))
		}
	}
	s := NewSession(cfg, cfg.Profiles["standard"])
	out := s.AnonymizeText("Mme Claire Dupont puis Dupnot a rappelé.")
	if strings.Contains(out, "Dupnot") || s.Restore(out) != "Mme Claire Dupont puis Dupont a rappelé." {
		t.Errorf("faute de frappe : %q → %q", out, s.Restore(out))
	}
}

// 10. a learned place or company is only found again with its capital letter.
func TestPlacesCapitalizedOnly(t *testing.T) {
	cfg := anonConfig(t)
	for _, in := range []string{"## Manage users\nYou can manage roles here.", "Mol fraction: 2 mol of NaCl.", "Mes jambes sont lourdes. Jambes : œdème."} {
		if out := NewSession(cfg, cfg.Profiles["standard"]).AnonymizeText(in); out != in {
			t.Errorf("faux positif : %q → %q", in, out)
		}
	}
	if out := NewSession(cfg, cfg.Profiles["standard"]).AnonymizeText("Train a Random Forest; the forest has 100 trees."); !strings.Contains(out, "the forest") {
		t.Errorf("forêt : %q", out)
	}
	if out := NewSession(cfg, cfg.Profiles["standard"]).AnonymizeText("Il habite à Forest. Forest est une commune."); strings.Contains(out, "Forest") {
		t.Errorf("commune : %q", out)
	}
	for _, p := range []string{"mol", "manage", "jambes"} {
		if placeSet[p] {
			t.Errorf("%q est encore dans la liste des localités", p)
		}
	}
}

// 11. encrypted blobs are opaque.
func TestEncryptedUntouched(t *testing.T) {
	cfg := anonConfig(t)
	for i := 0; i < 50; i++ {
		b := make([]byte, 3000)
		rand.Read(b)
		enc := base64.StdEncoding.EncodeToString(b)
		inner, _ := json.Marshal(map[string]any{"encrypted_content": enc, "note": "Mme Claire Dubois"})
		body := map[string]any{"model": "m", "messages": []any{
			map[string]any{"role": "user", "content": "cherche"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "web_search_tool_result", "tool_use_id": "srvtoolu_1", "content": []any{
					map[string]any{"type": "web_search_result", "url": "https://x", "title": "t", "encrypted_content": enc, "encrypted_index": enc}}}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t", "content": string(inner)}}}}}
		NewSession(cfg, cfg.Profiles["standard"]).Process(body, "anthropic")
		res := body["messages"].([]any)[1].(map[string]any)["content"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
		if res["encrypted_content"] != enc || res["encrypted_index"] != enc {
			t.Fatal("contenu chiffré modifié")
		}
		var v map[string]string
		json.Unmarshal([]byte(body["messages"].([]any)[2].(map[string]any)["content"].([]any)[0].(map[string]any)["content"].(string)), &v)
		if v["encrypted_content"] != enc || strings.Contains(v["note"], "Dubois") {
			t.Fatalf("JSON du résultat d'outil : %v", v["note"])
		}
	}
}

// 12. the prefix of a conversation (tools, system) keeps its tokens from turn to turn.
func TestStablePrefix(t *testing.T) {
	cfg := anonConfig(t)
	std := cfg.Profiles["standard"]
	sys := func(js string) string {
		body, _, _ := anonProcess(t, cfg, std, "anthropic", js)
		return fmt.Sprint(body["system"], body["tools"])
	}
	tools := `"tools":[{"name":"crm","description":"Support : support@acme.be","input_schema":{"type":"object"}}]`
	t1 := sys(`{"model":"m",` + tools + `,"system":"Utilisateur : jean.martin@acme.be","messages":[{"role":"user","content":"écris à a@b.be"}]}`)
	t2 := sys(`{"model":"m",` + tools + `,"system":"Utilisateur : jean.martin@acme.be","messages":[{"role":"user","content":"écris à a@b.be"},
		{"role":"assistant","content":"ok"},{"role":"user","content":"et à c@d.be"}]}`)
	if t1 != t2 || !strings.Contains(t1, "Support : «EMAIL_1»") || !strings.Contains(t1, "Utilisateur : «EMAIL_2»") {
		t.Errorf("préfixe instable :\n%s\n%s", t1, t2)
	}
	if !strings.Contains(sys(`{"model":"m","messages":[{"role":"user","content":"Bonjour"}]}`), "pseudonymisées") {
		t.Error("consigne placeholder_hint absente sans valeur remplacée")
	}
}

// 13. technical numbers and words are not personal data.
func TestTechnicalFalsePositivesNumbers(t *testing.T) {
	cfg := anonConfig(t)
	for ts := int64(1700000000000); ts < 1700000002000; ts++ { // millisecond timestamps
		if f := findTS(cfg, fmt.Sprintf("ts=%d", ts)); f != "" {
			t.Fatalf("horodatage %d pris pour %s", ts, f)
		}
	}
	for x := int64(31234567890); x < 31234569890; x++ { // 11-digit identifiers
		if len(findINAMI(fmt.Sprintf("id %d ok", x))) > 0 {
			t.Fatalf("identifiant %d pris pour un numéro INAMI", x)
		}
	}
	for _, in := range []string{"carte 4111 1111 1111 1111", "Amex 3782 822463 10005", "visa 4111111111111111", "Tel 0470 12 34 56",
		"prescripteur 12345625001", "Dr Martin, INAMI 1-23456-25-001"} {
		if out := NewSession(cfg, cfg.Profiles["standard"]).AnonymizeText(in); !strings.Contains(out, "«") {
			t.Errorf("non détecté : %q", in)
		}
	}
	for _, in := range []string{"lr = 0.0012345678 and eps = 0.00000123456", "Une réunion est prévue le 12/03/2026.", "The deadline is 12/03/2026.",
		"Done 12/03/2026, online since 01/02/2025.", "Je ne viendrai pas le 12/03/2026."} {
		if out := NewSession(cfg, cfg.Profiles["standard"]).AnonymizeText(in); out != in {
			t.Errorf("faux positif : %q → %q", in, out)
		}
	}
	for _, in := range []string{"né le 12/03/1990", "née 12/03/1990", "ne le 12/03/1990", "date of birth: 12/03/1990", "geboren op 12/03/1990", "Naissance : 12/03/1990"} {
		if len(findBirthdates(in)) != 1 {
			t.Errorf("date de naissance non détectée : %q", in)
		}
	}
}

func findTS(cfg *Config, in string) string {
	s := NewSession(cfg, cfg.Profiles["standard"])
	for _, sp := range s.spans(in, false) {
		return sp.kind
	}
	return ""
}

// 14. tokens written back with spaces inside the guillemets.
func TestRestoreSpacedGuillemets(t *testing.T) {
	cfg := anonConfig(t)
	s := NewSession(cfg, cfg.Profiles["standard"])
	s.AnonymizeText("écris à claire.dubois@acme.be")
	for _, r := range []string{"«EMAIL_1»", "« EMAIL_1 »", "«\u00a0EMAIL_1\u00a0»", "«\u202fEMAIL_1\u202f»"} {
		if got := s.Restore(r); got != "claire.dubois@acme.be" {
			t.Errorf("%q → %q", r, got)
		}
	}
}

// File names are pseudonymized before the file log, the alerts and the block message.
func TestFileNamesMasked(t *testing.T) {
	cfg := anonConfig(t)
	p := *cfg.Profiles["standard"]
	p.Files = map[string]string{"pdf": "block_alert", "txt": "pass_log"}
	js := `{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"Voici le bilan de Mme Dupont."},
		{"type":"file","file":{"filename":"Bilan_Dupont_90010112395.pdf","file_data":"data:application/pdf;base64,AAAA"}},
		{"type":"text","text":"--- Document joint : notes-claire.dubois@acme.be.txt ---\nok\n--- Fin du document ---"}]}]}`
	_, _, s := anonProcess(t, cfg, &p, "openai", js)
	var names []string
	for _, e := range s.FileLog {
		names = append(names, e.Name)
	}
	all := strings.Join(append(append(names, s.BlockedFiles...), s.AlertFiles...), " | ")
	noLeak(t, all, "Dupont", "90010112395", "claire.dubois")
	if len(s.AlertFiles) != 1 || !strings.HasPrefix(s.AlertFiles[0], "Bilan_«PERSON_") || !strings.HasSuffix(s.AlertFiles[0], "_«NISS_1».pdf") || len(names) != 2 {
		t.Errorf("noms de fichiers : %s", all)
	}
	if !strings.Contains(blockMessage(s), "Bilan_«") {
		t.Errorf("message de blocage : %s", blockMessage(s))
	}
}

// JSON documents in tool results stay valid whatever their escapes (ensure_ascii, nested JSON)
func TestJSONStaysValid(t *testing.T) {
	cfg := anonConfig(t)
	rnd := mrand.New(mrand.NewSource(3))
	vals := []any{"Mme Claire Dubois", "Gérald Keizer", "claire.dubois@acme.be", "+32 470 12 34 56", "0470\u00a012\u00a034\u00a056",
		"BE68 5390 0754 7034", "90.01.01-123.95", "Rappel :\nMme Lefèvre\t« urgent »", "C:\\Users\\Claire\\notes", "say \"Dupont\"",
		"né le 12/03/1990", json.Number("90010112395"), json.Number("4111111111111111"), json.Number("3"), true, nil}
	var gen func(depth int) any
	gen = func(depth int) any {
		switch k := rnd.Intn(6); {
		case k == 0 && depth < 3:
			m := map[string]any{}
			for i := 0; i < 1+rnd.Intn(4); i++ {
				k := []string{"nom", "note", "Claire Dubois", "tel", "data"}[rnd.Intn(5)]
				if rnd.Intn(12) == 0 {
					k = "encrypted_content"
				}
				m[k] = gen(depth + 1)
			}
			return m
		case k == 1 && depth < 3:
			var l []any
			for i := 0; i < 1+rnd.Intn(4); i++ {
				l = append(l, gen(depth+1))
			}
			return l
		case k == 2 && depth < 3: // JSON text inside a string
			return string(asciiJSON(gen(depth + 1)))
		}
		return vals[rnd.Intn(len(vals))]
	}
	for i := 0; i < 300; i++ {
		doc := asciiJSON(map[string]any{"r": gen(0)})
		body := map[string]any{"model": "m", "messages": []any{map[string]any{"role": "tool", "tool_call_id": "c", "content": string(doc)}}}
		NewSession(cfg, cfg.Profiles["standard"]).Process(body, "openai")
		out := lastMessage(body)["content"].(string)
		if !json.Valid([]byte(out)) {
			t.Fatalf("JSON invalide :\n%s\n%s", doc, out)
		}
		if !strings.Contains(string(doc), "encrypted") { // encrypted blobs are left as they are
			noLeak(t, out, "Dubois", "Keizer", "acme.be", "470 12", "5390", "123.95", "90010112395", "4111111111111111", "Lef")
		}
	}
}

// asciiJSON encodes like Python's json.dumps (ensure_ascii): every non-ASCII character escaped.
func asciiJSON(v any) []byte {
	b, _ := json.Marshal(v)
	var out strings.Builder
	for _, r := range string(b) {
		if r < 0x80 {
			out.WriteRune(r)
		} else {
			fmt.Fprintf(&out, "\\u%04x", r)
		}
	}
	return []byte(out.String())
}

// ---------- fast paths: same result as the plain implementation ----------

// every match of the plain word-list regex (word-bounded) must also be found by the fast path
func TestWordListEquivalence(t *testing.T) {
	cfg := anonConfig(t)
	rnd := mrand.New(mrand.NewSource(1))
	var terms []string
	terms = append(terms, healthOrgs...)
	terms = append(terms, "Claire Dubois", "Jean Martin", "Globex", "HUB", "CHU", "H.U.B.", "Kevin", "Strasse", "ſaint", "+32 470 12 34 56", "#tag")
	mut := func(w string) string {
		var b strings.Builder
		for _, r := range w {
			switch rnd.Intn(6) {
			case 0:
				r = unicode.ToUpper(r)
			case 1:
				r = unicode.ToLower(r)
			}
			if r == ' ' && rnd.Intn(3) == 0 {
				b.WriteString("  \n")
				continue
			}
			if r == 'K' && rnd.Intn(3) == 0 {
				r = '\u212a' // Kelvin sign: folds with k
			}
			b.WriteRune(r)
		}
		if isAllCaps(w) && rnd.Intn(2) == 0 {
			return strings.Join(strings.Split(w, ""), ".") + "."
		}
		return b.String()
	}
	seps := []string{" ", ", ", "(", ") ", "\n", "'", "-", "", "x", "«", "» ", "\u00a0", "_", "é"}
	var dets []*Detector
	for _, d := range cfg.dets {
		if d.wl != nil {
			dets = append(dets, d)
		}
	}
	dets = append(dets, &Detector{re: wordsRegexCase(terms, true), wl: newWordList(terms, true), words: true})
	n := 0
	for n < 20000 { // bounded by the number of comparisons, not by time
		var b strings.Builder
		for i := 0; i < 20; i++ {
			b.WriteString(seps[rnd.Intn(len(seps))])
			if rnd.Intn(2) == 0 {
				b.WriteString(mut(terms[rnd.Intn(len(terms))]))
			} else {
				b.WriteString(benchProse[rnd.Intn(len(benchProse)-20):][:rnd.Intn(20)])
			}
		}
		s := strings.ToValidUTF8(b.String(), "")
		for _, d := range dets {
			for k, w := range []*wordList{d.wl, d.wlCS} {
				re := []*regexp.Regexp{d.re, d.reCS}[k]
				if w == nil || re == nil {
					continue
				}
				fast := map[[2]int]bool{}
				for _, m := range w.find(s) {
					fast[[2]int{m[0], m[1]}] = true
				}
				for _, m := range re.FindAllStringIndex(s, -1) {
					if boundary(s, m[0], m[1]) && !fast[[2]int{m[0], m[1]}] {
						t.Fatalf("manqué %q dans %q", s[m[0]:m[1]], s)
					}
				}
				n++
			}
		}
	}
	t.Logf("%d comparaisons", n)
}

func TestStreetEquivalence(t *testing.T) {
	rnd := mrand.New(mrand.NewSource(2))
	parts := []string{"rue", "Rue", "RUE", "avenue", "Av.", "av", "chée", "Chée", "drève", "Boulevard", "bd", "straat", "Laan", "weg", "Street",
		"de la", "du", "van der", "Loi", "Louise", "Waterloo", "Émile", "Jacques Brel", "16", "54", "1180", "bte", "3", "b.", "12a",
		" ", "  ", ", ", "\n", "é", "x", "_", "-", "'", "Prérue", "ruelle", "rues", "\u00a0", "2"}
	n := 0
	for it := 0; it < 20000; it++ {
		var b strings.Builder
		for i := 0; i < 40; i++ {
			b.WriteString(parts[rnd.Intn(len(parts))])
			if rnd.Intn(2) == 0 {
				b.WriteString(" ")
			}
		}
		s := b.String()
		fast := map[[2]int]bool{}
		for _, m := range streetList.find(s) {
			fast[[2]int{m[0], m[1]}] = true
		}
		for _, m := range reStreet.FindAllStringIndex(s, -1) {
			if !fast[[2]int{m[0], m[1]}] {
				t.Fatalf("manqué %q dans %q", s[m[0]:m[1]], s)
			}
			n++
		}
	}
	t.Logf("%d adresses comparées", n)
}

// detectors run concurrently on large texts: same spans, in the same order
func TestParallelDetectorsDeterministic(t *testing.T) {
	cfg := anonConfig(t)
	txt := benchCorpus(64 << 10)
	if len(txt) < parallelMin {
		t.Fatal("texte trop court pour le chemin parallèle")
	}
	st, _ := OpenStore(filepath.Join(t.TempDir(), "m.db"))
	l, _ := NewLearner(st.db)
	learner = l
	stop, done := make(chan bool), make(chan bool)
	go func() { // the name memory keeps learning while the detectors read it (race detector)
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				l.Learn([]string{fmt.Sprintf("Vandam%c%c", 'a'+rune(i%26), 'a'+rune(i/26%26))})
				time.Sleep(time.Millisecond)
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
		learner = nil
	}()
	todo := make([]int, len(cfg.dets))
	for i := range todo {
		todo[i] = i
	}
	for round := 0; round < 3; round++ {
		res := make([][]span, len(cfg.dets))
		findAll(txt, cfg.dets, todo, res)
		for i, d := range cfg.dets {
			if want := d.Find(txt); !d.volatile() && !reflect.DeepEqual(res[i], want) {
				t.Fatalf("détecteur %s : résultats différents en parallèle", d.Kind)
			}
		}
	}
}

// the cross-request cache gives the same request as a fresh scan
func TestCrossRequestCache(t *testing.T) {
	cfg := anonConfig(t)
	st, _ := OpenStore(filepath.Join(t.TempDir(), "m.db"))
	learner, _ = NewLearner(st.db)
	defer func() { learner = nil }()
	var msgs []any
	for i := 0; i < 12; i++ {
		role := []string{"user", "assistant"}[i%2]
		msgs = append(msgs, map[string]any{"role": role, "content": fmt.Sprintf("Tour %d. ", i) + benchCorpus(1500)})
	}
	js, _ := json.Marshal(map[string]any{"model": "m", "system": "Utilisateur : jean.martin@acme.be. " + benchProse, "messages": msgs})
	run := func(c *spanCache) (string, map[string]string) {
		var body map[string]any
		json.Unmarshal(js, &body)
		s := NewSession(cfg, cfg.Profiles["standard"])
		s.cache = c
		s.Process(body, "anthropic")
		return string(encodeJSON(body)), s.rev
	}
	c := newSpanCache(1000, 1<<20)
	want, wrev := run(nil)
	for i := 0; i < 3; i++ {
		got, rev := run(c)
		if got != want || !reflect.DeepEqual(rev, wrev) {
			t.Fatalf("tour %d : résultat différent avec le cache", i)
		}
	}
	if c.hits == 0 {
		t.Error("cache jamais utilisé")
	}
	small := newSpanCache(3, 1<<20) // eviction
	for i := 0; i < 2; i++ {
		if got, _ := run(small); got != want {
			t.Fatal("résultat différent après éviction")
		}
	}
	if small.ll.Len() > 3 {
		t.Errorf("cache borné à 3 entrées : %d", small.ll.Len())
	}
}

// Forget only reloads the memory when something was deleted.
func TestLearnerForget(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewLearner(st.db)
	if err != nil {
		t.Fatal(err)
	}
	l.Learn([]string{"Vandenbroucke", "Keizer"})
	for i := 0; i < 100 && l.Count() < 2; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	idx := fmt.Sprintf("%p", l.idx)
	if err := l.Forget(365); err != nil || fmt.Sprintf("%p", l.idx) != idx || !l.Match("Keizer") {
		t.Fatalf("rien à oublier : l'index ne doit pas être rechargé (%v)", err)
	}
	st.db.Exec(`UPDATE learned SET last = ? WHERE h = ?`, time.Now().AddDate(-2, 0, 0).Unix(), int64(l.hash("name:keizer")))
	if err := l.Forget(365); err != nil || l.Match("Keizer") || !l.Match("Vandenbroucke") || l.Count() != 1 {
		t.Fatalf("oubli sélectif : %v, %d entrée(s)", err, l.Count())
	}
	if err := l.Forget(0); err != nil || l.Count() != 0 || l.Match("Vandenbroucke") {
		t.Fatalf("oubli total : %v", err)
	}
}

// ---------- benchmarks ----------

const benchProse = "Le patient a été reçu en consultation pour un suivi de son traitement. " +
	"Les résultats des analyses montrent une amélioration nette par rapport au mois dernier, " +
	"mais il convient de poursuivre la surveillance et d'adapter la posologie si nécessaire. " +
	"La réunion de coordination avec l'équipe soignante est prévue la semaine prochaine. "

// benchCorpus: text of ~n bytes, prose with one PII sentence every ~1 KB.
func benchCorpus(n int) string {
	var b strings.Builder
	for b.Len() < n {
		for i := 0; i < 3; i++ {
			b.WriteString(benchProse)
		}
		b.WriteString(pii + " ")
	}
	return strings.ToValidUTF8(b.String()[:n], "")
}

// benchNames: ~kb KB of text naming nNames distinct people (with email and phone), so that the
// session learns thousands of values.
func benchNames(nNames, kb int) string {
	firsts := []string{"Claire", "Jean", "Marc", "Sophie", "Luc", "Els", "Pieter", "Anne", "Thomas", "Julie"}
	var b strings.Builder
	for i := 0; b.Len() < kb<<10; i++ {
		f := firsts[i%len(firsts)]
		k := i % nNames
		sur := fmt.Sprintf("Vandam%c%c", 'a'+rune(k%26), 'a'+rune(k/26%26))
		fmt.Fprintf(&b, "Le patient %s %s (%s.%s@exemple.be, 0470 12 %02d %02d) a été vu le 12/03/2026 à Bruxelles. ",
			f, sur, strings.ToLower(f), strings.ToLower(sur), k%100, (k/100)%100)
		b.WriteString("Le serveur répond avec un code 200 ; voir handleRequest(ctx, req) dans main.go.\n")
		if i%4 == 0 {
			b.WriteString("```go\nfunc handle(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(map[string]any{\"ok\": true}) }\n```\n")
		}
	}
	return b.String()
}

func benchConfig(b *testing.B) *Config {
	b.Helper()
	cfg, err := LoadConfig("config.example.toml")
	if err != nil {
		b.Fatal(err)
	}
	return cfg
}

// benchSession: a session without the cross-request cache, so that each iteration scans the text.
func benchSession(cfg *Config) *Session {
	s := NewSession(cfg, cfg.Profiles["standard"])
	s.cache = nil
	return s
}

func benchAnon(b *testing.B, txt string, workers int) {
	cfg := benchConfig(b)
	detectWorkers = workers
	defer func() { detectWorkers = 0 }()
	b.SetBytes(int64(len(txt)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSession(cfg).AnonymizeText(txt)
	}
}

func BenchmarkAnon1KB(b *testing.B)   { benchAnon(b, benchCorpus(1<<10), 0) }
func BenchmarkAnon10KB(b *testing.B)  { benchAnon(b, benchCorpus(10<<10), 0) }
func BenchmarkAnon100KB(b *testing.B) { benchAnon(b, benchCorpus(100<<10), 0) }

// one core: the detectors of a large text are not run concurrently
func BenchmarkAnon100KBOneCore(b *testing.B) { benchAnon(b, benchCorpus(100<<10), 1) }

func BenchmarkAnon10KBParallel(b *testing.B) {
	cfg := benchConfig(b)
	txt := benchCorpus(10 << 10)
	b.SetBytes(int64(len(txt)))
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			benchSession(cfg).AnonymizeText(txt)
		}
	})
}

// 110 KB naming ~480 people: ~2000 values learned by the session.
func BenchmarkAnonLearned2000(b *testing.B) {
	cfg := benchConfig(b)
	txt := benchNames(800, 110)
	s := benchSession(cfg)
	s.AnonymizeText(txt)
	b.SetBytes(int64(len(txt)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSession(cfg).AnonymizeText(txt)
	}
	b.ReportMetric(float64(len(s.learn)), "learned")
}

// Claude Code style traffic: the whole history (40 messages, ~100 KB) is sent again at every turn.
func benchHistory(b *testing.B, cache bool) {
	cfg := benchConfig(b)
	var msgs []string
	for i := 0; i < 40; i++ {
		msgs = append(msgs, fmt.Sprintf("Tour %d. ", i)+benchCorpus(2500))
	}
	body := func() map[string]any {
		var ms []any
		for i, m := range msgs {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			ms = append(ms, map[string]any{"role": role, "content": m})
		}
		return map[string]any{"model": "m", "system": "Tu es utile.", "messages": ms}
	}
	b.SetBytes(40 * 2500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		bd := body()
		s := NewSession(cfg, cfg.Profiles["standard"])
		if !cache {
			s.cache = nil
		}
		b.StartTimer()
		s.Process(bd, "anthropic")
	}
}

func BenchmarkProcessHistory100KB(b *testing.B)        { benchHistory(b, true) }
func BenchmarkProcessHistory100KBNoCache(b *testing.B) { benchHistory(b, false) }
