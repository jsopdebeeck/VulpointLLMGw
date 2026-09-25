package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Gateway struct {
	cfg    atomic.Pointer[Config]
	store  *Store
	meter  *Meter
	client *http.Client
	saveMu sync.Mutex
}

func (g *Gateway) C() *Config { return g.cfg.Load() }

func ms(d time.Duration) int64 { return d.Milliseconds() }

// ---------- JSON helpers ----------

func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}

func encodeJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func num(v any) int64 {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		f, _ := x.Float64()
		return int64(f)
	case float64:
		return int64(x)
	}
	return 0
}

func takeUsage(u map[string]any, rec *Rec) {
	if u == nil {
		return
	}
	if v, ok := u["prompt_tokens"]; ok {
		rec.TokIn = num(v)
	}
	if v, ok := u["completion_tokens"]; ok {
		rec.TokOut = num(v)
	}
	if v, ok := u["input_tokens"]; ok {
		rec.TokIn = num(v) + num(u["cache_creation_input_tokens"]) + num(u["cache_read_input_tokens"])
	}
	if v, ok := u["output_tokens"]; ok {
		rec.TokOut = num(v)
	}
}

func apiError(w http.ResponseWriter, api string, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var v any
	if api == "anthropic" {
		v = map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}}
	} else {
		v = map[string]any{"error": map[string]any{"type": typ, "code": typ, "message": msg}}
	}
	w.Write(encodeJSON(v))
}

// ---------- auth & routing ----------

func clientKey(r *http.Request) string {
	if k := r.Header.Get("x-api-key"); k != "" {
		return k
	}
	if a := r.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
		return strings.TrimSpace(a[7:])
	}
	return ""
}

func (g *Gateway) auth(cfg *Config, r *http.Request) (string, *User) {
	name, ok := cfg.keys[clientKey(r)]
	if !ok {
		return "", nil
	}
	if u := cfg.Users[name]; !u.Disabled {
		return name, u
	}
	return name, nil
}

type cand struct {
	name  string
	t     *Target
	model string
}

// route returns the allowed targets for this model, in profile order (fallback chain).
// "target/model" pins a target explicitly.
func route(cfg *Config, p *Profile, api, model string) []cand {
	if i := strings.Index(model, "/"); i > 0 {
		if t, ok := cfg.Targets[model[:i]]; ok {
			for _, n := range p.Targets {
				if n == model[:i] && t.API == api && !t.Disabled && t.Allows(model[i+1:]) {
					return []cand{{n, t, model[i+1:]}}
				}
			}
			return nil
		}
	}
	var out []cand
	for _, n := range p.Targets {
		if t := cfg.Targets[n]; t.API == api && !t.Disabled && t.Allows(model) {
			out = append(out, cand{n, t, model})
		}
	}
	return out
}

func (g *Gateway) send(in *http.Request, c cand, api string, payload []byte) (*http.Response, error) {
	url := strings.TrimRight(c.t.BaseURL, "/")
	if api == "openai" {
		url += "/chat/completions"
	} else {
		url += "/v1/messages"
	}
	req, err := http.NewRequestWithContext(in.Context(), "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if api == "anthropic" {
		v := in.Header.Get("anthropic-version")
		if v == "" {
			v = "2023-06-01"
		}
		req.Header.Set("anthropic-version", v)
		if b := in.Header.Get("anthropic-beta"); b != "" {
			req.Header.Set("anthropic-beta", b)
		}
		if c.t.key != "" {
			req.Header.Set("x-api-key", c.t.key)
		}
	} else if c.t.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.t.key)
	}
	for k, v := range c.t.Headers {
		req.Header.Set(k, v)
	}
	return g.client.Do(req)
}

// ---------- main handler: IN -> anonymize -> route -> OUT -> restore ----------

func (g *Gateway) handleLLM(api string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t0 := time.Now()
		cfg := g.C()
		rec := &Rec{TS: t0, API: api, User: "?"}
		var inDur, upDur time.Duration
		admitted := false
		rec.Dets = map[string]map[string]int{}
		quotaEvent := func(limit, act string) {
			k := "QUOTA_" + limit
			if rec.Dets[k] == nil {
				rec.Dets[k] = map[string]int{}
			}
			rec.Dets[k][act]++
		}
		defer func() {
			rec.TotalMs, rec.InMs, rec.UpMs = ms(time.Since(t0)), ms(inDur), ms(upDur)
			rec.GwMs = max(0, rec.TotalMs-rec.InMs-rec.UpMs)
			if admitted {
				g.meter.AddTokens(rec.TokIn+rec.TokOut, "u:"+rec.User, "t:"+rec.Target)
			}
			g.store.Add(rec)
			log.Printf("%s %s → %s/%s %d in=%dms gw=%dms llm=%dms tok=%d/%d conv=%d viol=%d %s",
				api, rec.User, rec.Target, rec.Model, rec.Status, rec.InMs, rec.GwMs, rec.UpMs, rec.TokIn, rec.TokOut, rec.Anon, rec.Blocked, rec.Err)
		}()

		name, u := g.auth(cfg, r)
		if u == nil {
			if name != "" {
				rec.User = name
			}
			rec.Status, rec.Err = 401, "auth"
			apiError(w, api, 401, "authentication_error", "clé API gateway invalide ou utilisateur désactivé")
			return
		}
		rec.User, rec.Profile = name, u.Profile
		prof := cfg.Profiles[u.Profile]

		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(cfg.Server.MaxBodyMB)<<20))
		inDur = time.Since(t0)
		rec.BytesIn = int64(len(raw))
		if err != nil {
			rec.Status, rec.Err = 413, "body"
			apiError(w, api, 413, "invalid_request_error", "corps de requête illisible ou trop volumineux")
			return
		}
		v, err := decodeJSON(raw)
		body, ok := v.(map[string]any)
		if err != nil || !ok {
			rec.Status, rec.Err = 400, "json"
			apiError(w, api, 400, "invalid_request_error", "JSON invalide")
			return
		}
		model, _ := body["model"].(string)
		stream, _ := body["stream"].(bool)
		rec.Model, rec.Stream = model, stream

		cands := route(cfg, prof, api, model)
		if len(cands) == 0 {
			rec.Status, rec.Err = 403, "route"
			apiError(w, api, 403, "permission_error", fmt.Sprintf("modèle %q non autorisé pour le profil %q", model, u.Profile))
			return
		}
		rec.Target = cands[0].name

		uq := cfg.EffectiveQuota(u)
		if lim := g.meter.Exceeded("u:"+name, &uq); lim != "" {
			if uq.Action == "warn" {
				quotaEvent(lim, "log")
			} else {
				quotaEvent(lim, "block")
				rec.Status, rec.Err, rec.Blocked = 429, "quota", 1
				apiError(w, api, 429, "rate_limit_error", quotaMsg("utilisateur", name, lim))
				return
			}
		}
		// targets over their own quota are skipped (next target in the profile takes over)
		var ok2 []cand
		for _, c := range cands {
			if lim := g.meter.Exceeded("t:"+c.name, c.t.Quota); lim != "" {
				if c.t.Quota.Action == "warn" {
					quotaEvent(lim, "log")
				} else {
					quotaEvent(lim, "block")
					continue
				}
			}
			ok2 = append(ok2, c)
		}
		if len(ok2) == 0 {
			rec.Status, rec.Err, rec.Blocked = 429, "quota", 1
			apiError(w, api, 429, "rate_limit_error", "quota atteint pour tous les LLM autorisés")
			return
		}
		cands = ok2
		rec.Target = cands[0].name

		sess := NewSession(cfg, prof)
		sess.Process(body, api)
		for k, m := range sess.Stats {
			rec.Dets[k] = m
		}
		rec.Anon, rec.Blocked = sess.Anon, sess.Blocked
		if sess.Blocked > 0 {
			rec.Status, rec.Err = 403, "policy"
			apiError(w, api, 403, "permission_error", "requête bloquée par la politique de données : "+sess.BlockedKinds())
			return
		}

		admitted = true
		g.meter.Admit("u:" + name)

		clientSO, hadSO := body["stream_options"]
		var resp *http.Response
		var upStart time.Time
		var fails []string
		for i, c := range cands {
			body["model"] = c.model
			delete(body, "stream_options")
			if hadSO {
				body["stream_options"] = clientSO
			}
			if api == "openai" && stream && c.t.StreamUsage {
				so := map[string]any{}
				if m, ok := clientSO.(map[string]any); ok {
					for k, v := range m {
						so[k] = v
					}
				}
				so["include_usage"] = true
				body["stream_options"] = so
			}
			rec.Target, rec.Model = c.name, c.model
			upStart = time.Now()
			resp, err = g.send(r, c, api, encodeJSON(body))
			last := i == len(cands)-1
			if err == nil && (last || (resp.StatusCode < 500 && resp.StatusCode != 429)) {
				rec.TtfbMs = ms(time.Since(upStart))
				break
			}
			if err == nil {
				resp.Body.Close()
				fails = append(fails, fmt.Sprintf("%s:%d", c.name, resp.StatusCode))
			} else {
				fails = append(fails, c.name+":"+err.Error())
			}
			resp = nil
		}
		rec.Err = strings.Join(fails, " ")
		if resp == nil {
			upDur = time.Since(upStart)
			rec.Status = 502
			apiError(w, api, 502, "api_error", "aucun LLM cible disponible : "+rec.Err)
			return
		}
		defer resp.Body.Close()
		rec.Status = resp.StatusCode
		g.meter.Admit("t:" + rec.Target)
		w.Header().Set("X-Gateway-Target", rec.Target+"/"+rec.Model)
		w.Header().Set("X-Gateway-Conversions", fmt.Sprint(sess.Anon))
		w.Header().Set("X-Gateway-Kinds", sess.ConvertedKinds())

		if stream && resp.StatusCode == 200 && strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(200)
			if err := relaySSE(w, resp.Body, api, sess, rec); err != nil {
				rec.Err = strings.TrimSpace(rec.Err + " stream:" + err.Error())
			}
		} else {
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				rec.Err = strings.TrimSpace(rec.Err + " read:" + err.Error())
			}
			out := data
			if v, err := decodeJSON(data); err == nil {
				restoreAll(v, sess.Restore)
				if m, ok := v.(map[string]any); ok {
					u, _ := m["usage"].(map[string]any)
					takeUsage(u, rec)
				}
				out = encodeJSON(v)
			} else {
				out = []byte(sess.Restore(string(data)))
			}
			ct := resp.Header.Get("Content-Type")
			if ct == "" {
				ct = "application/json"
			}
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(resp.StatusCode)
			w.Write(out)
			rec.BytesOut = int64(len(out))
		}
		upDur = time.Since(upStart)
	}
}

// ---------- SSE relay with on-the-fly restore ----------

func relaySSE(w http.ResponseWriter, body io.Reader, api string, s *Session, rec *Rec) error {
	fl, _ := w.(http.Flusher)
	rs := newRestorer(s)
	var tpl map[string]any
	put := func(name, data string, extra []string) {
		var b strings.Builder
		for _, l := range extra {
			b.WriteString(l + "\n")
		}
		if name != "" {
			b.WriteString("event: " + name + "\n")
		}
		b.WriteString("data: " + data + "\n\n")
		n, _ := io.WriteString(w, b.String())
		rec.BytesOut += int64(n)
	}
	flushAll := func() {
		for _, k := range rs.Keys(nil) {
			if left := rs.Flush(k); left != "" {
				if api == "openai" {
					put("", string(encodeJSON(oaiChunk(tpl, k, left))), nil)
				} else {
					put("content_block_delta", string(encodeJSON(anthDelta(k.a, k.f, left))), nil)
				}
			}
		}
	}
	handle := func(lines []string) {
		var name string
		var data, extra []string
		for _, l := range lines {
			switch {
			case strings.HasPrefix(l, "event:"):
				name = strings.TrimSpace(l[6:])
			case strings.HasPrefix(l, "data:"):
				data = append(data, strings.TrimPrefix(l[5:], " "))
			default:
				extra = append(extra, l)
			}
		}
		if data == nil {
			n, _ := io.WriteString(w, strings.Join(lines, "\n")+"\n\n")
			rec.BytesOut += int64(n)
			return
		}
		d := strings.Join(data, "\n")
		if d == "[DONE]" {
			flushAll()
			put(name, d, extra)
			return
		}
		v, err := decodeJSON([]byte(d))
		m, ok := v.(map[string]any)
		if err != nil || !ok {
			put(name, s.Restore(d), extra)
			return
		}
		if api == "openai" {
			tpl = m
			oaiEvent(m, rs, rec)
		} else {
			for _, pre := range anthEvent(m, rs, rec) {
				put("content_block_delta", string(encodeJSON(pre)), nil)
			}
		}
		put(name, string(encodeJSON(m)), extra)
	}

	br := bufio.NewReaderSize(body, 64<<10)
	var lines []string
	for {
		line, err := br.ReadString('\n')
		if t := strings.TrimRight(line, "\r\n"); t != "" {
			lines = append(lines, t)
		} else if line != "" && len(lines) > 0 {
			handle(lines)
			lines = nil
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			if len(lines) > 0 {
				handle(lines)
			}
			flushAll()
			if fl != nil {
				fl.Flush()
			}
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func oaiEvent(m map[string]any, rs *Restorer, rec *Rec) {
	if u, ok := m["usage"].(map[string]any); ok {
		takeUsage(u, rec)
	}
	chs, _ := m["choices"].([]any)
	for _, c := range chs {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		ci := int(num(cm["index"]))
		delta, _ := cm["delta"].(map[string]any)
		if delta != nil {
			for _, f := range []string{"content", "reasoning_content", "reasoning"} { // DeepSeek, GLM, Grok, vLLM
				if t, ok := delta[f].(string); ok {
					delta[f] = rs.Push(rkey{ci, -1, f}, t)
				}
			}
			tcs, _ := delta["tool_calls"].([]any)
			for _, tc := range tcs {
				if tm, ok := tc.(map[string]any); ok {
					if f, ok := tm["function"].(map[string]any); ok {
						if a, ok := f["arguments"].(string); ok {
							f["arguments"] = rs.Push(rkey{ci, int(num(tm["index"])), "arguments"}, a)
						}
					}
				}
			}
		}
		if cm["finish_reason"] == nil {
			continue
		}
		for _, k := range rs.Keys(func(k rkey) bool { return k.a == ci }) {
			left := rs.Flush(k)
			if left == "" {
				continue
			}
			if delta == nil {
				delta = map[string]any{}
				cm["delta"] = delta
			}
			if k.f != "arguments" {
				prev, _ := delta[k.f].(string)
				delta[k.f] = prev + left
				continue
			}
			tcs, _ := delta["tool_calls"].([]any)
			found := false
			for _, tc := range tcs {
				if tm, ok := tc.(map[string]any); ok && int(num(tm["index"])) == k.b {
					f, _ := tm["function"].(map[string]any)
					if f == nil {
						f = map[string]any{}
						tm["function"] = f
					}
					prev, _ := f["arguments"].(string)
					f["arguments"], found = prev+left, true
				}
			}
			if !found {
				delta["tool_calls"] = append(tcs, map[string]any{"index": k.b, "function": map[string]any{"arguments": left}})
			}
		}
	}
}

func oaiChunk(tpl map[string]any, k rkey, left string) map[string]any {
	c := map[string]any{}
	for _, f := range []string{"id", "object", "created", "model", "system_fingerprint"} {
		if v, ok := tpl[f]; ok {
			c[f] = v
		}
	}
	delta := map[string]any{}
	if k.f != "arguments" {
		delta[k.f] = left
	} else {
		delta["tool_calls"] = []any{map[string]any{"index": k.b, "function": map[string]any{"arguments": left}}}
	}
	c["choices"] = []any{map[string]any{"index": k.a, "delta": delta, "finish_reason": nil}}
	return c
}

func anthEvent(m map[string]any, rs *Restorer, rec *Rec) []map[string]any {
	switch m["type"] {
	case "message_start":
		if msg, ok := m["message"].(map[string]any); ok {
			u, _ := msg["usage"].(map[string]any)
			takeUsage(u, rec)
			restoreAll(msg, rs.s.Restore)
		}
	case "message_delta":
		u, _ := m["usage"].(map[string]any)
		takeUsage(u, rec)
	case "content_block_delta":
		idx := int(num(m["index"]))
		if d, ok := m["delta"].(map[string]any); ok {
			for _, f := range []string{"text", "partial_json"} {
				if t, ok := d[f].(string); ok {
					d[f] = rs.Push(rkey{idx, 0, f}, t)
				}
			}
		}
	case "content_block_stop":
		idx := int(num(m["index"]))
		var pre []map[string]any
		for _, k := range rs.Keys(func(k rkey) bool { return k.a == idx }) {
			if left := rs.Flush(k); left != "" {
				pre = append(pre, anthDelta(idx, k.f, left))
			}
		}
		return pre
	default:
		restoreAll(m, rs.s.Restore)
	}
	return nil
}

func anthDelta(idx int, f, left string) map[string]any {
	typ := "text_delta"
	if f == "partial_json" {
		typ = "input_json_delta"
	}
	return map[string]any{"type": "content_block_delta", "index": idx, "delta": map[string]any{"type": typ, f: left}}
}

// ---------- /v1/models ----------

func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	cfg := g.C()
	_, u := g.auth(cfg, r)
	if u == nil {
		apiError(w, "openai", 401, "authentication_error", "clé API gateway invalide")
		return
	}
	data := []any{}
	seen := map[string]bool{}
	for _, n := range cfg.Profiles[u.Profile].Targets {
		for _, m := range cfg.Targets[n].Models {
			if strings.Contains(m, "*") {
				continue
			}
			for _, id := range []string{m, n + "/" + m} {
				if !seen[id] {
					seen[id] = true
					data = append(data, map[string]any{"id": id, "object": "model", "owned_by": n})
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(encodeJSON(map[string]any{"object": "list", "data": data}))
}

// ---------- /v1/me : what the calling user may do (chat window) ----------

func (g *Gateway) handleMe(w http.ResponseWriter, r *http.Request) {
	cfg := g.C()
	name, u := g.auth(cfg, r)
	if u == nil {
		apiError(w, "openai", 401, "authentication_error", "clé API gateway invalide ou utilisateur désactivé")
		return
	}
	p := cfg.Profiles[u.Profile]
	type tgt struct {
		Name, API string
		Models    []string
	}
	var ts []tgt
	for _, n := range p.Targets {
		if t := cfg.Targets[n]; !t.Disabled {
			ts = append(ts, tgt{n, t.API, t.Models})
		}
	}
	us := g.meter.Snapshot()["u:"+name]
	writeJSON(w, map[string]any{"user": name, "description": u.Description, "profile": u.Profile,
		"profileDescription": p.Description, "images": p.Images, "targets": ts,
		"quota": cfg.EffectiveQuota(u), "usage": us})
}
