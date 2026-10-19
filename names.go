package main

import (
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/binary"
	"hash"
	"io"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Name heuristics: typo-tolerant titles, context cues, first-name dictionary,
// and a persistent memory of names that stores only keyed hashes.

//go:embed firstnames.txt
var firstnamesTxt string

//go:embed lexicon.txt.gz
var lexiconGz []byte

// lexicon: common (lowercase) English dictionary words. A capitalized common word
// ("Transport", "Custom", "Provider") is not taken for a surname on weak evidence.
var lexicon = func() map[string]bool {
	m := map[string]bool{}
	zr, err := gzip.NewReader(bytes.NewReader(lexiconGz))
	if err != nil {
		return m
	}
	b, _ := io.ReadAll(zr)
	for _, w := range strings.Split(string(b), "\n") {
		if w != "" {
			m[w] = true
		}
	}
	for _, w := range surnameWords {
		delete(m, w)
	}
	return m
}()

// surnames that are also dictionary words: still accepted as names
var surnameWords = []string{"martin", "rose", "baker", "smith", "miller", "carpenter", "cook", "hunter", "wood", "young",
	"king", "bishop", "fisher", "mason", "turner", "walker", "white", "black", "brown", "green", "gray", "grey", "bell",
	"hill", "noble", "blanc", "petit", "lambert", "roland", "vincent", "marchand", "baron", "prince", "page", "gentil"}

// commonWord: dictionary word, acronym, product or all-caps token: not a name without strong evidence.
func commonWord(w string) bool {
	f := fold(w)
	if lexicon[f] || defaultAllow[f] || notNames[f] {
		return true
	}
	// the dictionary holds base forms: providers, settings, configured, endpoints…
	for _, suf := range []string{"s", "es", "ed", "d", "ing", "ers", "er"} {
		if b := strings.TrimSuffix(f, suf); b != f && len(b) >= 4 && (lexicon[b] || lexicon[b+"e"]) {
			return true
		}
	}
	return false
}

//go:embed allow_default.txt
var allowDefaultTxt string

// defaultAllow: products, vendors and acronyms that are never person names (folded).
var defaultAllow = func() map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(allowDefaultTxt, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			m[fold(l)] = true
		}
	}
	return m
}()

var firstnames = func() map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(firstnamesTxt, "\n") {
		if strings.HasPrefix(l, "#") {
			continue
		}
		for _, w := range strings.Fields(l) {
			m[fold(w)] = true
		}
	}
	return m
}()

var (
	// titles are matched with one typo allowed when they have 5+ letters (Moniseur, Madmae, Doctuer…)
	titles = []string{"monsieur", "madame", "mademoiselle", "docteur", "maitre", "professeur", "messieurs", "mesdames",
		"mister", "missis", "doctor", "professor", "mevrouw", "meneer", "juffrouw", "dokter", "herr", "frau"}
	shortTitles = map[string]bool{"m": true, "mr": true, "mrs": true, "ms": true, "mme": true, "mmes": true, "mlle": true,
		"dr": true, "pr": true, "prof": true, "me": true, "dhr": true, "mevr": true, "sr": true, "sra": true}
	// context cues: the next capitalized words are a name
	cues = map[string]bool{"patient": true, "patiente": true, "client": true, "cliente": true, "nomme": true, "nommee": true,
		"appele": true, "appelee": true, "appelle": true, "epoux": true, "epouse": true, "veuve": true, "veuf": true,
		"ne": true, "nee": true, "signe": true, "signee": true, "beneficiaire": true, "assure": true, "assuree": true,
		"collaborateur": true, "collaboratrice": true, "employe": true, "employee": true, "candidat": true, "candidate": true,
		"named": true, "called": true, "patiënt": true, "genaamd": true, "cliënt": true}
	particles = map[string]bool{"de": true, "du": true, "des": true, "d": true, "van": true, "der": true, "den": true,
		"von": true, "le": true, "la": true, "del": true, "della": true, "di": true, "da": true, "dos": true, "ten": true, "ter": true}
	// capitalized words that must never be taken for a surname
	notNames = map[string]bool{"le": true, "la": true, "les": true, "un": true, "une": true, "je": true, "il": true, "elle": true,
		"nous": true, "vous": true, "ils": true, "elles": true, "est": true, "et": true, "ou": true, "the": true, "and": true,
		"this": true, "that": true, "is": true, "was": true, "bonjour": true, "merci": true, "cordialement": true}
)

var foldMap = map[rune]rune{'à': 'a', 'â': 'a', 'ä': 'a', 'á': 'a', 'ã': 'a', 'å': 'a', 'ç': 'c', 'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e',
	'î': 'i', 'ï': 'i', 'í': 'i', 'ì': 'i', 'ô': 'o', 'ö': 'o', 'ó': 'o', 'ò': 'o', 'õ': 'o', 'ù': 'u', 'û': 'u', 'ü': 'u', 'ú': 'u',
	'ÿ': 'y', 'ñ': 'n', 'œ': 'o', 'æ': 'a', 'ß': 's'}

// fold lowercases and strips accents: "Gérald" -> "gerald".
func fold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if f, ok := foldMap[r]; ok {
			r = f
		}
		b.WriteRune(r)
	}
	return b.String()
}

type tok struct {
	s, e int
	w    string
}

func isNameRune(r rune) bool { return unicode.IsLetter(r) || r == '\'' || r == '’' || r == '-' }

// isGlue: symbols that a broken PDF font puts in place of letters ("J@an—S©basti@" for
// "Jean-Sébastien"). Right after a letter they belong to the word; dashes only between
// two letters. Markdown marks (*, #, `, |) are never glued.
func isGlue(s string, i int, r rune) bool {
	switch r {
	case '@', '©', '®', '¤', '§', '°', '¬', '¦', '±', 'µ', '¨', '´', '\uFFFD':
		return r != '@' || !emailAt(s, i)
	case '—', '–':
		n, _ := utf8.DecodeRuneInString(s[i+utf8.RuneLen(r):])
		return unicode.IsLetter(n)
	}
	return false
}

func tokenize(s string) []tok {
	var out []tok
	start := -1
	for i, r := range s {
		if isNameRune(r) || start >= 0 && isGlue(s, i, r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			out = append(out, trimTok(s, start, i))
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, trimTok(s, start, len(s)))
	}
	return out
}

// emailAt: an "@" between two letters followed by a domain is an email, not a damaged letter.
func emailAt(s string, i int) bool {
	rest := s[i+1:]
	if j := strings.IndexFunc(rest, func(c rune) bool { return !(unicode.IsLetter(c) || unicode.IsDigit(c) || c == '-' || c == '.') }); j >= 0 {
		rest = rest[:j]
	}
	return strings.Contains(strings.Trim(rest, "."), ".")
}

func trimTok(s string, a, b int) tok {
	w := strings.Trim(s[a:b], "-'’")
	a += strings.Index(s[a:b], w)
	return tok{a, a + len(w), w}
}

func isCap(w string) bool { r, _ := utf8.DecodeRuneInString(w); return unicode.IsUpper(r) }
func isAllCaps(w string) bool {
	n := 0
	for _, r := range w {
		if unicode.IsLetter(r) {
			if !unicode.IsUpper(r) {
				return false
			}
			n++
		}
	}
	return n >= 2
}

// gapOK: only spaces between two tokens of the same name (prev, then gap); a dot only after an
// initial ("J. Dupont") or an abbreviated title ("Dr. Peeters"), not at the end of a sentence
// ("Le patient Dupont. Demain…").
func gapOK(prev, gap string) bool {
	dot := false
	for _, r := range gap {
		switch r {
		case ' ', '\u00a0', '\t':
		case '.':
			dot = true
		default:
			return false
		}
	}
	if len(gap) > 3 {
		return false
	}
	return !dot || utf8.RuneCountInString(prev) == 1 && isCap(prev) || shortTitles[fold(prev)]
}

// editWithin1 reports whether a and b differ by at most one insertion, deletion,
// substitution or adjacent transposition.
func editWithin1(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}
	switch len(ra) - len(rb) {
	case 0:
		var diff []int
		for i := range ra {
			if ra[i] != rb[i] {
				diff = append(diff, i)
			}
		}
		return len(diff) <= 1 || (len(diff) == 2 && diff[1] == diff[0]+1 && ra[diff[0]] == rb[diff[1]] && ra[diff[1]] == rb[diff[0]])
	case 1:
		i := 0
		for i < len(rb) && ra[i] == rb[i] {
			i++
		}
		return string(ra[i+1:]) == string(rb[i:])
	}
	return false
}

func isTitle(w string) bool {
	f := strings.TrimSuffix(fold(w), ".")
	if shortTitles[f] { // abbreviations need their capital: "me", "m" are ordinary French words
		return isCap(w)
	}
	n := utf8.RuneCountInString(f)
	if n < 5 || n > 13 { // titles have 4 to 12 letters, one typo allowed
		return false
	}
	for _, t := range titles {
		if d := n - len(t); d < -1 || d > 1 { // titles are ASCII: len is their letter count
			continue
		}
		if f == t || editWithin1(f, t) {
			// lowercase is only a title for forms of address ("bonjour monsieur Keizer");
			// "maître", "docteur", "professeur" are also ordinary nouns ("le fichier maître Portugal")
			return isCap(w) || addressForms[t]
		}
	}
	return false
}

var addressForms = map[string]bool{"monsieur": true, "madame": true, "mademoiselle": true, "messieurs": true, "mesdames": true,
	"mevrouw": true, "meneer": true, "juffrouw": true}

// nameAfter returns the end of the name that starts at token i (1 to 3 capitalized
// words, particles allowed inside), or -1.
func nameAfter(s string, ts []tok, i int) (int, int) { return nameAfterMode(s, ts, i, false) }

// nameAfterMode: strict (weak evidence, e.g. a first name) refuses common words as surnames.
// With a title or a context word, the name may start with allowed words ("Mme Claude Dubois",
// "patient Hermes Vandamme"); allowed() then drops the names made of allowed words only.
func nameAfterMode(s string, ts []tok, i int, strict bool) (int, int) {
	last, n, real := -1, 0, false
	for j := i; j < len(ts) && n < 3; j++ {
		if j > i && !gapOK(ts[j-1].w, s[ts[j-1].e:ts[j].s]) {
			break
		}
		w := ts[j].w
		al := defaultAllow[fold(w)]
		if isCap(w) && utf8.RuneCountInString(w) >= 2 && !notNames[fold(w)] && !isTitle(w) && !(al && (strict || real)) && !(strict && commonWord(w)) {
			last, n, real = j, n+1, real || !al
			continue
		}
		if particles[fold(w)] && last >= 0 || particles[fold(w)] && j == i {
			continue
		}
		break
	}
	if last < 0 {
		return -1, 0
	}
	return last, n
}

// findTitled: "Moniseur Gérald Keizer", "patient Keizer", "Dhr. Janssens".
func findTitled(s string) [][2]int {
	ts := tokenize(s)
	var out [][2]int
	for i := 0; i < len(ts)-1; i++ {
		f := fold(ts[i].w)
		if !isTitle(ts[i].w) && !(cues[f] && !isAllCaps(ts[i].w)) { // "CLIENT LIST" is a heading, not a cue
			continue
		}
		if f == "m" && !strings.HasPrefix(s[ts[i].e:], ".") { // "M." only
			continue
		}
		if !gapOK(ts[i].w, s[ts[i].e:ts[i+1].s]) {
			continue
		}
		if last, _ := nameAfter(s, ts, i+1); last >= 0 {
			out = append(out, [2]int{ts[i+1].s, ts[last].e})
			i = last
		}
	}
	return out
}

// findFirstnames: known first name followed by a surname ("Gérald Keizer", "Jean-Pierre van Damme"),
// or an all-caps surname followed by a first name ("KEIZER Gérald").
func findFirstnames(s string) [][2]int {
	ts := tokenize(s)
	var out [][2]int
	isFirst := func(w string) bool {
		if !isCap(w) || defaultAllow[fold(w)] {
			return false
		}
		f := fold(w)
		if firstnames[f] {
			return true
		}
		for _, p := range strings.Split(f, "-") { // Jean-Pierre, Marie-Claire
			if !firstnames[p] {
				return false
			}
		}
		return strings.Contains(f, "-")
	}
	for i := 0; i < len(ts); i++ {
		if !isFirst(ts[i].w) {
			continue
		}
		if i > 0 && isAllCaps(ts[i-1].w) && utf8.RuneCountInString(ts[i-1].w) >= 3 && !commonWord(ts[i-1].w) &&
			gapOK(ts[i-1].w, s[ts[i-1].e:ts[i].s]) {
			out = append(out, [2]int{ts[i-1].s, ts[i].e})
			continue
		}
		if i+1 < len(ts) && gapOK(ts[i].w, s[ts[i].e:ts[i+1].s]) {
			if last, _ := nameAfterMode(s, ts, i+1, true); last >= 0 {
				out = append(out, [2]int{ts[i].s, ts[last].e})
				i = last
			}
		}
	}
	return out
}

// nameTokens: the words of a detected name worth remembering.
func nameTokens(v string) []string {
	var out []string
	for _, t := range tokenize(v) {
		f := fold(t.w)
		if utf8.RuneCountInString(f) >= 4 && isCap(t.w) && !particles[f] && !isTitle(t.w) && !commonWord(t.w) && !isAllCaps(t.w) {
			out = append(out, t.w)
		}
	}
	return out
}

// deletion variants: two words within one typo share at least one variant.
func variants(f string) []string {
	rs := []rune(f)
	out := []string{f}
	if len(rs) < 5 { // short names: exact match only
		return out
	}
	for i := range rs {
		out = append(out, string(rs[:i])+string(rs[i+1:]))
	}
	return out
}

// ---------- persistent, privacy-preserving name memory ----------

// Learner remembers names seen with high confidence. Only HMAC-SHA256 hashes
// (keyed by a secret stored in the database) of the folded name and of its
// one-deletion variants are kept: the names themselves are never written.
type Learner struct {
	mu    sync.RWMutex // idx and seen
	wmu   sync.Mutex   // database writes: the writer goroutine and Forget
	db    *sql.DB
	key   []byte            // set once by NewLearner
	idx   map[uint64]uint64 // variant hash -> name hash
	seen  map[uint64]bool
	queue chan []string
}

var learner *Learner // nil when disabled

func NewLearner(db *sql.DB) (*Learner, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v BLOB);
		CREATE TABLE IF NOT EXISTS learned(h INTEGER PRIMARY KEY, n INTEGER, first INTEGER, last INTEGER, vars BLOB);`); err != nil {
		return nil, err
	}
	l := &Learner{db: db, queue: make(chan []string, 1000)}
	if err := db.QueryRow(`SELECT v FROM meta WHERE k='learn_key'`).Scan(&l.key); err != nil {
		l.key = make([]byte, 32)
		rand.Read(l.key)
		if _, err := db.Exec(`INSERT INTO meta VALUES('learn_key', ?)`, l.key); err != nil {
			return nil, err
		}
	}
	var err error
	if l.idx, l.seen, err = l.load(); err != nil {
		return nil, err
	}
	go l.writer()
	return l, nil
}

// load reads the memory from the database.
func (l *Learner) load() (map[uint64]uint64, map[uint64]bool, error) {
	idx, seen := map[uint64]uint64{}, map[uint64]bool{}
	rs, err := l.db.Query(`SELECT h, vars FROM learned`)
	if err != nil {
		return nil, nil, err
	}
	defer rs.Close()
	for rs.Next() {
		var h int64
		var vars []byte
		rs.Scan(&h, &vars)
		seen[uint64(h)] = true
		for i := 0; i+8 <= len(vars); i += 8 {
			idx[binary.BigEndian.Uint64(vars[i:])] = uint64(h)
		}
	}
	return idx, seen, rs.Err()
}

func (l *Learner) hash(s string) uint64 {
	m := hmac.New(sha256.New, l.key)
	m.Write([]byte(s))
	return binary.BigEndian.Uint64(m.Sum(nil))
}

// candidate: a word that may be a remembered name.
func (l *Learner) candidate(w string) bool {
	if !isCap(w) || utf8.RuneCountInString(w) < 4 {
		return false
	}
	f := fold(w)
	return !particles[f] && !isTitle(w) && !commonWord(w)
}

// Match reports whether a capitalized word is (within one typo of) a remembered name.
func (l *Learner) Match(w string) bool {
	if l == nil || !l.candidate(w) {
		return false
	}
	var hs []uint64
	for _, v := range variants(fold(w)) {
		hs = append(hs, l.hash(v))
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, h := range hs {
		if _, ok := l.idx[h]; ok {
			return true
		}
	}
	return false
}

// Find may run concurrently with the other detectors: the hashes are computed without the
// lock, which is only held for the lookups.
func (l *Learner) Find(s string) [][2]int {
	if l == nil || l.Count() == 0 {
		return nil
	}
	type cand struct {
		s, e int
		hs   []uint64
	}
	var cs []cand
	var m hash.Hash
	var sum []byte
	memo := map[string][]uint64{}
	for _, t := range tokenize(s) {
		if !l.candidate(t.w) {
			continue
		}
		f := fold(t.w)
		hs, ok := memo[f]
		if !ok {
			if m == nil {
				m = hmac.New(sha256.New, l.key)
			}
			for _, v := range variants(f) {
				m.Reset()
				m.Write([]byte(v))
				sum = m.Sum(sum[:0])
				hs = append(hs, binary.BigEndian.Uint64(sum))
			}
			memo[f] = hs
		}
		cs = append(cs, cand{t.s, t.e, hs})
	}
	if len(cs) == 0 {
		return nil
	}
	var out [][2]int
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, c := range cs {
		for _, h := range c.hs {
			if _, ok := l.idx[h]; ok {
				out = append(out, [2]int{c.s, c.e})
				break
			}
		}
	}
	return out
}

func (l *Learner) Learn(words []string) {
	if l == nil || len(words) == 0 {
		return
	}
	select {
	case l.queue <- words:
	default:
	}
}

func (l *Learner) writer() {
	for words := range l.queue {
		l.wmu.Lock()
		now := time.Now().Unix()
		for _, w := range words {
			f := fold(w)
			h := l.hash("name:" + f)
			var vars []byte
			vh := map[uint64]bool{}
			for _, v := range variants(f) {
				x := l.hash(v)
				vh[x] = true
				vars = binary.BigEndian.AppendUint64(vars, x)
			}
			l.mu.Lock()
			for x := range vh {
				l.idx[x] = h
			}
			l.seen[h] = true
			l.mu.Unlock()
			if _, err := l.db.Exec(`INSERT INTO learned VALUES(?,1,?,?,?) ON CONFLICT(h) DO UPDATE SET n=n+1, last=excluded.last`,
				int64(h), now, now, vars); err != nil {
				log.Print("mémoire des noms : ", err)
			}
		}
		l.wmu.Unlock()
	}
}

func (l *Learner) Count() int {
	if l == nil {
		return 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.seen)
}

// Forget erases the memory (all, or entries unused for more than days). The index is only
// reloaded when entries were deleted; it is rebuilt without the lock, then swapped, so that
// detection never sees an empty memory nor waits for the database.
func (l *Learner) Forget(days int) error {
	if l == nil {
		return nil
	}
	l.wmu.Lock()
	defer l.wmu.Unlock()
	q, args := `DELETE FROM learned`, []any{}
	if days > 0 {
		q, args = `DELETE FROM learned WHERE last < ?`, []any{time.Now().AddDate(0, 0, -days).Unix()}
	}
	res, err := l.db.Exec(q, args...)
	if err != nil {
		return err
	}
	idx, seen := map[uint64]uint64{}, map[uint64]bool{}
	if days > 0 {
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return nil
		}
		if idx, seen, err = l.load(); err != nil {
			return err
		}
	}
	l.mu.Lock()
	l.idx, l.seen = idx, seen
	l.mu.Unlock()
	return nil
}
