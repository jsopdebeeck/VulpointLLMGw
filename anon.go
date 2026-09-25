package main

import (
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ---------- detectors ----------

type Detector struct {
	Kind    string
	re      *regexp.Regexp
	group   int
	valid   func(string) bool
	words   bool              // enforce word boundaries (lists)
	names   bool              // matches are person/company names: learn their tokens too
	kinds   map[string]string // learned detector: lower(value) -> kind
	capOnly map[string]bool   // learned name words: only match when capitalized
	prio    int
	custom  func(string) [][2]int // heuristic finders (names)
	builtin string
}

type span struct {
	s, e  int
	kind  string
	prio  int
	canon string // typo of a value seen earlier: reuse its placeholder
}

type builtin struct {
	re    string
	group int
	valid func(string) bool
	names bool
	find  func(string) [][2]int
}

var builtins = map[string]builtin{
	"email":            {re: `[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`},
	"phone":            {re: `(?:\+|\b00)\d{2,3}[\s./-]?(?:\(0\)[\s./-]?)?\d(?:[\s./-]?\d){6,10}\b|\b0\d(?:[\s./-]?\d){7,8}\b`, valid: digitsBetween(8, 15)},
	"iban":             {re: `\b[A-Z]{2}\d{2}(?:[ ]?[A-Z0-9]{4}){2,7}(?:[ ]?[A-Z0-9]{1,3})?\b`, valid: validIBAN},
	"niss":             {re: `\b\d{2}\.?\d{2}\.?\d{2}[-.\s]?\d{3}[.\s]?\d{2}\b`, valid: validNISS},
	"card":             {re: `\b(?:\d[ -]?){12,18}\d\b`, valid: validLuhn},
	"ipv4":             {re: `\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b`},
	"person_title":     {find: findTitled, names: true},
	"person_firstname": {find: findFirstnames, names: true},
	"person_learned":   {find: func(s string) [][2]int { return learner.Find(s) }, names: true},
}

func buildDetector(dc *DetectorCfg, base string, prio int) (*Detector, error) {
	d := &Detector{Kind: strings.ToUpper(dc.Name), group: dc.Group, prio: prio}
	if !kindRe.MatchString(d.Kind) {
		return nil, fmt.Errorf("name doit être en MAJUSCULES_ET_CHIFFRES")
	}
	var err error
	switch {
	case dc.Builtin != "":
		b, ok := builtins[dc.Builtin]
		if !ok {
			return nil, fmt.Errorf("builtin inconnu %q", dc.Builtin)
		}
		d.group, d.valid, d.names, d.custom, d.builtin = b.group, b.valid, b.names, b.find, dc.Builtin
		if b.find == nil {
			d.re = regexp.MustCompile(b.re)
		}
	case dc.Regex != "":
		d.re, err = regexp.Compile(dc.Regex)
	case len(dc.Words) > 0 || dc.WordsFile != "":
		words := dc.Words
		if dc.WordsFile != "" {
			p := dc.WordsFile
			if !strings.HasPrefix(p, "/") {
				p = base + "/" + p
			}
			more, e := readWords(p)
			if e != nil {
				return nil, e
			}
			words = append(append([]string{}, words...), more...)
		}
		d.re, d.words, d.names = wordsRegex(words), true, true
	default:
		return nil, fmt.Errorf("builtin, regex ou words requis")
	}
	return d, err
}

func wordsRegex(words []string) *regexp.Regexp {
	ws := make([]string, 0, len(words))
	for _, w := range words {
		if w = strings.TrimSpace(w); w != "" {
			ws = append(ws, w)
		}
	}
	if len(ws) == 0 {
		return regexp.MustCompile(`a^`) // never matches
	}
	sort.Slice(ws, func(i, j int) bool { return len(ws[i]) > len(ws[j]) })
	for i, w := range ws {
		ws[i] = strings.ReplaceAll(regexp.QuoteMeta(w), " ", `\s+`)
	}
	return regexp.MustCompile(`(?i)(?:` + strings.Join(ws, "|") + `)`)
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

func boundary(s string, a, b int) bool {
	if a > 0 {
		if r, _ := utf8.DecodeLastRuneInString(s[:a]); isWordRune(r) {
			return false
		}
	}
	if b < len(s) {
		if r, _ := utf8.DecodeRuneInString(s[b:]); isWordRune(r) {
			return false
		}
	}
	return true
}

func (d *Detector) Find(s string) []span {
	var out []span
	if d.custom != nil {
		for _, m := range d.custom(s) {
			out = append(out, span{m[0], m[1], d.Kind, d.prio, ""})
		}
		return out
	}
	for _, m := range d.re.FindAllStringSubmatchIndex(s, -1) {
		a, b := m[0], m[1]
		if d.group > 0 && len(m) > 2*d.group+1 && m[2*d.group] >= 0 {
			a, b = m[2*d.group], m[2*d.group+1]
		}
		if a == b || (d.words && !boundary(s, a, b)) || (d.valid != nil && !d.valid(s[a:b])) {
			continue
		}
		if d.capOnly[strings.ToLower(s[a:b])] && !isCap(s[a:b]) {
			continue
		}
		k := d.Kind
		if d.kinds != nil {
			k = d.kinds[strings.ToLower(s[a:b])]
		}
		out = append(out, span{a, b, k, d.prio, ""})
	}
	return out
}

// ---------- validators ----------

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func digitsBetween(lo, hi int) func(string) bool {
	return func(s string) bool { n := len(onlyDigits(s)); return n >= lo && n <= hi }
}

func validNISS(s string) bool {
	d := onlyDigits(s)
	if len(d) != 11 {
		return false
	}
	var n9, cc int64
	fmt.Sscan(d[:9], &n9)
	fmt.Sscan(d[9:], &cc)
	return 97-n9%97 == cc || 97-(2000000000+n9)%97 == cc
}

func validIBAN(s string) bool {
	s = strings.ReplaceAll(s, " ", "")
	if len(s) < 15 || len(s) > 34 {
		return false
	}
	var b strings.Builder
	for _, r := range s[4:] + s[:4] {
		if r >= 'A' && r <= 'Z' {
			fmt.Fprintf(&b, "%d", r-'A'+10)
		} else {
			b.WriteRune(r)
		}
	}
	n, ok := new(big.Int).SetString(b.String(), 10)
	return ok && new(big.Int).Mod(n, big.NewInt(97)).Int64() == 1
}

func validLuhn(s string) bool {
	d := onlyDigits(s)
	if len(d) < 13 || len(d) > 19 {
		return false
	}
	sum := 0
	for i := range d {
		n := int(d[len(d)-1-i] - '0')
		if i%2 == 1 {
			if n *= 2; n > 9 {
				n -= 9
			}
		}
		sum += n
	}
	return sum%10 == 0
}

// ---------- per-request session: anonymize in, restore out ----------

type Session struct {
	prof      *Profile
	dets      []*Detector
	learned   *Detector
	learn     map[string]string // lower(value) -> kind
	fwd       map[string]string // kind \x00 lower(value) -> placeholder id
	rev       map[string]string // placeholder id -> original value
	cnt       map[string]int
	fuzzy     map[string]string // one-deletion variant of a learned name word -> lower(word)
	allow     map[string]bool
	nameWords map[string]bool // learned values that are names (case-sensitive match)
	toLearn   []string
	learnOn   bool // a person_learned detector is configured
	NoLearn   bool
	Stats     map[string]map[string]int // kind -> action -> count
	Anon      int
	Blocked   int
}

func NewSession(cfg *Config, prof *Profile) *Session {
	s := &Session{prof: prof, dets: cfg.dets, allow: cfg.allow, nameWords: map[string]bool{}, learn: map[string]string{}, fwd: map[string]string{},
		rev: map[string]string{}, cnt: map[string]int{}, fuzzy: map[string]string{}, Stats: map[string]map[string]int{}}
	for _, d := range cfg.dets {
		s.learnOn = s.learnOn || d.builtin == "person_learned"
	}
	return s
}

func (s *Session) action(kind string) string {
	if a, ok := s.prof.Actions[kind]; ok {
		return a
	}
	return s.prof.DefaultAction
}

func (s *Session) count(kind, act string) {
	if s.Stats[kind] == nil {
		s.Stats[kind] = map[string]int{}
	}
	s.Stats[kind][act]++
	switch act {
	case "anonymize", "strip":
		s.Anon++
	case "block":
		s.Blocked++
	}
}

func (s *Session) BlockedKinds() string {
	var ks []string
	for k, m := range s.Stats {
		if m["block"] > 0 {
			ks = append(ks, k)
		}
	}
	sort.Strings(ks)
	return strings.Join(ks, ", ")
}

// ConvertedKinds counts distinct pseudonymized values (and stripped media) per kind,
// e.g. "EMAIL:1,PERSON:2" (ASCII: sent as an HTTP header).
func (s *Session) ConvertedKinds() string {
	n := map[string]int{}
	for id := range s.rev {
		n[id[:strings.LastIndex(id, "_")]]++
	}
	for k, m := range s.Stats {
		n[k] += m["strip"]
	}
	var ks []string
	for k, c := range n {
		if c > 0 {
			ks = append(ks, fmt.Sprintf("%s:%d", k, c))
		}
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

func (s *Session) spans(text string, withLearned bool) []span {
	var all []span
	for _, d := range s.dets {
		if s.action(d.Kind) != "off" {
			all = append(all, d.Find(text)...)
		}
	}
	if withLearned && s.learned != nil {
		all = append(all, s.learned.Find(text)...)
	}
	if withLearned && len(s.fuzzy) > 0 { // typos of names seen in this conversation
		for _, t := range tokenize(text) {
			if !isCap(t.w) || utf8.RuneCountInString(t.w) < 5 {
				continue
			}
			for _, v := range variants(fold(t.w)) {
				if c, ok := s.fuzzy[v]; ok {
					all = append(all, span{t.s, t.e, s.learn[c], len(s.dets) + 1, c})
					break
				}
			}
		}
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.s != b.s {
			return a.s < b.s
		}
		if a.e-a.s != b.e-b.s {
			return a.e-a.s > b.e-b.s
		}
		return a.prio < b.prio
	})
	out, end := all[:0], 0
	for _, sp := range all {
		if s.allowed(text[sp.s:sp.e], sp.kind) {
			continue
		}
		if sp.s >= end {
			out = append(out, sp)
			end = sp.e
		}
	}
	return out
}

// allowed: the value, or for names its first word ("Claude Code", "Claude Cowork"), is on the allow list.
func (s *Session) allowed(v, kind string) bool {
	if len(s.allow) == 0 {
		return false
	}
	if s.allow[fold(v)] {
		return true
	}
	if s.isNames(kind) {
		if ts := tokenize(v); len(ts) > 0 && s.allow[fold(ts[0].w)] {
			return true
		}
	}
	return false
}

// learnFrom (pass 1) remembers every value to anonymize, so that a name found
// once ("Mme Dupont") is also replaced where it appears alone ("Dupont").
func (s *Session) learnFrom(text string) string {
	for _, sp := range s.spans(text, false) {
		if s.action(sp.kind) != "anonymize" {
			continue
		}
		v := text[sp.s:sp.e]
		s.learn[strings.ToLower(v)] = sp.kind
		if s.isNames(sp.kind) {
			for _, t := range strings.FieldsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || r == '-' }) {
				if r, _ := utf8.DecodeRuneInString(t); utf8.RuneCountInString(t) >= 3 && unicode.IsUpper(r) && !particles[fold(t)] {
					lt := strings.ToLower(t)
					if _, ok := s.learn[lt]; !ok {
						s.learn[lt] = sp.kind
						s.nameWords[lt] = true
					}
					for _, vr := range variants(fold(t)) {
						if len(vr) >= 4 {
							s.fuzzy[vr] = lt
						}
					}
				}
			}
			s.toLearn = append(s.toLearn, nameTokens(v)...)
		}
	}
	return text
}

func (s *Session) isNames(kind string) bool {
	for _, d := range s.dets {
		if d.Kind == kind && d.names {
			return true
		}
	}
	return false
}

func (s *Session) buildLearned() {
	if len(s.learn) == 0 {
		return
	}
	ws := make([]string, 0, len(s.learn))
	for w := range s.learn {
		ws = append(ws, w)
	}
	s.learned = &Detector{re: wordsRegex(ws), words: true, kinds: s.learn, capOnly: s.nameWords, prio: len(s.dets)}
}

func (s *Session) placeholder(kind, v string) string {
	k := kind + "\x00" + strings.ToLower(v)
	id, ok := s.fwd[k]
	if !ok {
		s.cnt[kind]++
		id = fmt.Sprintf("%s_%d", kind, s.cnt[kind])
		s.fwd[k], s.rev[id] = id, v
	}
	return "«" + id + "»"
}

// Anonymize (pass 2) applies the profile's action to every detected value.
func (s *Session) Anonymize(text string) string {
	sps := s.spans(text, true)
	if len(sps) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, sp := range sps {
		act := s.action(sp.kind)
		s.count(sp.kind, act)
		if act == "anonymize" {
			v := text[sp.s:sp.e]
			if sp.canon != "" {
				v = sp.canon
			}
			b.WriteString(text[last:sp.s])
			b.WriteString(s.placeholder(sp.kind, v))
			last = sp.e
		}
	}
	b.WriteString(text[last:])
	return b.String()
}

var phRe = regexp.MustCompile(`«\s?([A-Z][A-Z0-9_]*_\d+)\s?»`)

func (s *Session) Restore(t string) string {
	if len(s.rev) == 0 || !strings.Contains(t, "«") {
		return t
	}
	return phRe.ReplaceAllStringFunc(t, func(m string) string {
		if v, ok := s.rev[phRe.FindStringSubmatch(m)[1]]; ok {
			return v
		}
		return m
	})
}

const hintText = "Note : certaines données ont été pseudonymisées et remplacées par des jetons de la forme «TYPE_n» (ex. «PERSON_1», «EMAIL_2»). Reprends ces jetons exactement tels quels, guillemets « » compris, sans les traduire, les modifier ni les commenter."

// Process anonymizes a request body in place (both API formats).
func (s *Session) Process(body map[string]any, api string) {
	s.media(body)
	walk(body, 0, s.learnFrom)
	s.buildLearned()
	walk(body, 0, s.Anonymize)
	if s.prof.Hint && len(s.rev) > 0 {
		injectHint(body, api)
	}
	s.commitLearning()
}

// commitLearning feeds the persistent (hashed) name memory.
func (s *Session) commitLearning() {
	if s.learnOn && !s.NoLearn {
		learner.Learn(s.toLearn)
	}
}

// AnonymizeText is Process for a bare string (dashboard test panel).
func (s *Session) AnonymizeText(t string) string {
	s.learnFrom(t)
	s.buildLearned()
	return s.Anonymize(t)
}

func injectHint(body map[string]any, api string) {
	if api == "anthropic" {
		switch sys := body["system"].(type) {
		case string:
			body["system"] = sys + "\n\n" + hintText
		case []any:
			body["system"] = append(sys, map[string]any{"type": "text", "text": hintText})
		default:
			body["system"] = hintText
		}
		return
	}
	msgs, _ := body["messages"].([]any)
	body["messages"] = append([]any{map[string]any{"role": "system", "content": hintText}}, msgs...)
}

var mediaKinds = map[string]string{"image_url": "IMAGE", "image": "IMAGE", "input_image": "IMAGE",
	"document": "FILE", "file": "FILE", "input_file": "FILE", "input_audio": "AUDIO"}

func (s *Session) media(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(x) {
			x[k] = s.media(x[k])
		}
	case []any:
		for i, c := range x {
			if m, ok := c.(map[string]any); ok {
				if t, _ := m["type"].(string); mediaKinds[t] != "" {
					kind := mediaKinds[t]
					s.count(kind, s.prof.Images)
					if s.prof.Images == "strip" {
						x[i] = map[string]any{"type": "text", "text": "[" + strings.ToLower(kind) + " retiré(e) par le gateway]"}
					}
					continue
				}
			}
			x[i] = s.media(c)
		}
	}
	return v
}

// ---------- JSON walking ----------

var (
	textKeys = map[string]bool{"content": true, "text": true, "system": true, "prompt": true, "instructions": true}
	rawKeys  = map[string]bool{"input": true, "arguments": true} // tool inputs: every string is data
	skipKeys = map[string]bool{"type": true, "role": true, "id": true, "model": true, "media_type": true, "url": true,
		"data": true, "tool_use_id": true, "tool_call_id": true, "name": true, "cache_control": true, "source": true,
		"image_url": true, "file_id": true, "thinking": true, "signature": true}
	keepKeys = map[string]bool{"thinking": true, "signature": true, "data": true} // never rewritten (signed / binary)
)

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks) // deterministic numbering => stable prompts for upstream caching
	return ks
}

// walk applies fn to the user-content strings of a request. mode: 0 structure, 1 text, 2 raw.
func walk(v any, mode int, fn func(string) string) any {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(x) {
			if mode != 2 && skipKeys[k] {
				continue
			}
			m := mode
			if rawKeys[k] {
				m = 2
			} else if m == 0 && textKeys[k] {
				m = 1
			}
			x[k] = walk(x[k], m, fn)
		}
	case []any:
		for i := range x {
			x[i] = walk(x[i], mode, fn)
		}
	case string:
		if mode > 0 {
			return fn(x)
		}
	}
	return v
}

// restoreAll applies fn to every string of a response (placeholders are unambiguous).
func restoreAll(v any, fn func(string) string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			if !keepKeys[k] {
				x[k] = restoreAll(c, fn)
			}
		}
	case []any:
		for i := range x {
			x[i] = restoreAll(x[i], fn)
		}
	case string:
		return fn(x)
	}
	return v
}

// ---------- streaming restore: holds back a possibly split «PLACEHOLDER» ----------

type rkey struct {
	a, b int
	f    string
}

type Restorer struct {
	s   *Session
	buf map[rkey]string
}

func newRestorer(s *Session) *Restorer { return &Restorer{s: s, buf: map[rkey]string{}} }

func (r *Restorer) Push(k rkey, t string) string {
	t = r.buf[k] + t
	cut := len(t)
	if i := strings.LastIndex(t, "«"); i >= 0 && !strings.Contains(t[i:], "»") && len(t)-i <= 64 {
		cut = i
	}
	if cut < len(t) {
		r.buf[k] = t[cut:]
	} else {
		delete(r.buf, k)
	}
	return r.s.Restore(t[:cut])
}

func (r *Restorer) Flush(k rkey) string {
	t := r.buf[k]
	delete(r.buf, k)
	return r.s.Restore(t)
}

func (r *Restorer) Keys(filter func(rkey) bool) []rkey {
	var ks []rkey
	for k := range r.buf {
		if filter == nil || filter(k) {
			ks = append(ks, k)
		}
	}
	sort.Slice(ks, func(i, j int) bool {
		if ks[i].a != ks[j].a {
			return ks[i].a < ks[j].a
		}
		if ks[i].b != ks[j].b {
			return ks[i].b < ks[j].b
		}
		return ks[i].f < ks[j].f
	})
	return ks
}
