package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Conversation memory. A client that names its conversation (X-LLMGW-Session header, sent by
// the Hermes plugin; Claude Code's session id is read from metadata.user_id) gets the same
// placeholders for the whole conversation: «PERSON_1» stays the same person from one turn to
// the next, and a name learned at one turn is still recognised later, even after the client
// compacted its history. The state lives in memory only, per user, profile and conversation,
// and is forgotten after convTTL without activity.

const (
	convTTL       = 24 * time.Hour
	convMax       = 5000 // conversations kept
	convMaxValues = 5000 // values per conversation: beyond, new values are not remembered
)

type convState struct {
	mu                       sync.Mutex
	learn, surface, fwd, rev map[string]string
	fuzzy                    map[string]string
	capOnly                  map[string]bool
	cnt                      map[string]int
	last                     time.Time
}

type convStore struct {
	mu sync.Mutex
	m  map[string]*convState
}

var conversations = &convStore{m: map[string]*convState{}}

// get returns the state of a conversation, created if needed, locked: the caller unlocks it once
// the request is anonymized and the state saved, so two requests of the same conversation never
// hand out the same placeholder to different values.
func (c *convStore) get(key string) *convState {
	c.mu.Lock()
	st := c.m[key]
	if st == nil {
		if len(c.m) >= convMax {
			c.evict()
		}
		st = &convState{}
		c.m[key] = st
	}
	st.last = time.Now()
	c.mu.Unlock()
	st.mu.Lock()
	return st
}

// evict drops the idle conversations, then the oldest tenth if still full. Callers hold c.mu.
func (c *convStore) evict() {
	now := time.Now()
	for k, st := range c.m {
		if now.Sub(st.last) > convTTL {
			delete(c.m, k)
		}
	}
	if len(c.m) < convMax {
		return
	}
	type kv struct {
		k string
		t time.Time
	}
	all := make([]kv, 0, len(c.m))
	for k, st := range c.m {
		all = append(all, kv{k, st.last})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	for _, x := range all[:len(all)/10+1] {
		delete(c.m, x.k)
	}
}

func (c *convStore) gc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, st := range c.m {
		if now.Sub(st.last) > convTTL {
			delete(c.m, k)
		}
	}
}

func copyMap[V any](m map[string]V) map[string]V {
	out := make(map[string]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Resume starts a request from what the conversation already knows (copies: the request may
// fail and must not leave half a state behind).
func (s *Session) Resume(st *convState) {
	if st.rev == nil {
		return
	}
	s.learn, s.surface, s.fwd, s.rev = copyMap(st.learn), copyMap(st.surface), copyMap(st.fwd), copyMap(st.rev)
	s.fuzzy, s.capOnly, s.cnt = copyMap(st.fuzzy), copyMap(st.capOnly), copyMap(st.cnt)
}

// Save keeps what this request learned for the next turns of the conversation.
func (s *Session) Save(st *convState) {
	if len(s.rev) > convMaxValues {
		return // runaway conversation: keep the previous state rather than grow without bound
	}
	st.learn, st.surface, st.fwd, st.rev = s.learn, s.surface, s.fwd, s.rev
	st.fuzzy, st.capOnly, st.cnt = s.fuzzy, s.capOnly, s.cnt
}

// ---------- conversation and client identification ----------

var (
	convIDRe      = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	claudeSession = regexp.MustCompile(`session_([0-9a-fA-F-]{8,64})`)
)

// conversationID: the conversation named by the client, if any: the X-LLMGW-Session header,
// or the session id Claude Code puts in metadata.user_id. Anything else is hashed.
func conversationID(r *http.Request, body map[string]any) string {
	id := strings.TrimSpace(r.Header.Get("X-LLMGW-Session"))
	if id == "" {
		if md, ok := body["metadata"].(map[string]any); ok {
			if u, ok := md["user_id"].(string); ok {
				if m := claudeSession.FindStringSubmatch(u); m != nil {
					id = "claude-code:" + m[1]
				}
			}
		}
	}
	if id == "" || convIDRe.MatchString(id) {
		return id
	}
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:16])
}

func convKey(user, profile, id string) string { return user + "\x00" + profile + "\x00" + id }

// clientName: which application sent the request, for the reporting (X-LLMGW-Client first,
// then well-known User-Agents; the chat page is recognised by its own requests).
func clientName(r *http.Request) string {
	if c := strings.TrimSpace(r.Header.Get("X-LLMGW-Client")); c != "" {
		c = strings.ToLower(strings.Fields(c)[0])
		if i := strings.IndexByte(c, '/'); i > 0 {
			c = c[:i]
		}
		return clip(userNameRe.ReplaceAllString(c, "-"), 32)
	}
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	switch {
	case r.Header.Get(sessionHeader) != "" || strings.HasSuffix(r.Header.Get("Referer"), "/chat"):
		return "chat"
	case strings.Contains(ua, "claude-cli") || strings.Contains(ua, "claude-code"):
		return "claude-code"
	case strings.Contains(ua, "claude") && strings.Contains(ua, "cowork"):
		return "claude-cowork"
	case strings.Contains(ua, "hermes"):
		return "hermes"
	case strings.Contains(ua, "openai/python") || strings.Contains(ua, "openai/js"):
		return "openai-sdk"
	case strings.Contains(ua, "anthropic/python") || strings.Contains(ua, "anthropic/js"):
		return "anthropic-sdk"
	case ua == "":
		return "inconnu"
	}
	return "autre"
}

// convSummary: what a conversation keeps, for GET /v1/me (counts only, never the values).
func (c *convStore) summary(key, id string) map[string]any {
	c.mu.Lock()
	st := c.m[key]
	exp := time.Now().Add(convTTL)
	if st != nil {
		exp = st.last.Add(convTTL)
	}
	c.mu.Unlock()
	kinds := map[string]int{}
	n := 0
	if st != nil {
		st.mu.Lock()
		for k, v := range st.cnt {
			kinds[k], n = v, n+v
		}
		st.mu.Unlock()
	}
	return map[string]any{"id": id, "values": n, "kinds": kinds, "expires": exp.Unix()}
}
