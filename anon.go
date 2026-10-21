package main

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"
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
	kinds   map[string]string // learned detector: learnKey(value) -> kind
	capOnly map[string]bool   // learned detector: values that only match when capitalized
	prio    int
	custom  func(string) [][2]int // heuristic finders (names)
	reCS    *regexp.Regexp        // case-sensitive list terms (acronyms that are also words)
	wl      *wordList             // fast path for re when it is a word list
	wlCS    *wordList             // fast path for reCS
	builtin string
}

// volatile: the result depends on mutable state (the persistent name memory), so it is never cached.
func (d *Detector) volatile() bool { return d.builtin == "person_learned" }

type span struct {
	s, e  int
	kind  string
	prio  int
	canon string    // typo of a value seen earlier: reuse its placeholder
	det   *Detector // nil for values learned during the request
	v     string    // the value as the detectors saw it (normalized text)
}

type builtin struct {
	re    string
	group int
	valid func(string) bool
	names bool
	find  func(string) [][2]int
	words []string // built-in term list
}

var builtins = map[string]builtin{
	"email":            {re: `[\p{L}\p{N}._%+\-]+@[\p{L}\p{N}\-]+(?:\.[\p{L}\p{N}\-]+)*\.[A-Za-z]{2,}`},
	"phone":            {find: findPhones},
	"iban":             {find: findIBAN},
	"niss":             {re: `\b\d{2}[ ./-]?\d{2}[ ./-]?\d{2}[-./\s]?\d{3}[-.\s]?\d{2}\b`, valid: validNISS},
	"inami":            {find: findINAMI},
	"card":             {re: `\b(?:\d[ -]?){12,18}\d\b`, valid: validCard},
	"ipv4":             {re: `\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b`},
	"person_title":     {find: findTitled, names: true},
	"person_firstname": {find: findFirstnames, names: true},
	"person_learned":   {find: func(s string) [][2]int { return learner.Find(s) }, names: true},
	"ref_context":      {find: findRefContext},
	"address":          {find: findAddresses},
	"place":            {find: findPlaces},
	"pet":              {find: findPets},
	"firstname_alone":  {find: findFirstnameAlone, names: true},
	"be_health_orgs":   {words: healthOrgs},
	"birthdate":        {find: findBirthdates},
	"date":             {find: findDates},
}

func buildDetector(dc *DetectorCfg, base string, prio int) (*Detector, error) {
	d := &Detector{Kind: strings.ToUpper(dc.Name), group: dc.Group, prio: prio}
	if !kindRe.MatchString(d.Kind) {
		return nil, fmt.Errorf("name doit être en MAJUSCULES_ET_CHIFFRES")
	}
	var err error
	switch {
	case dc.Builtin != "" && builtins[dc.Builtin].words != nil: // built-in list: same handling as words
		return buildDetector(&DetectorCfg{Name: dc.Name, Words: append(append([]string{}, builtins[dc.Builtin].words...), dc.Words...)}, base, prio)
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
		// an all-caps term is an acronym: it only matches in capitals (HUB, not "hub");
		// list the lowercase form explicitly to catch it too
		var ci, cs []string
		for _, w := range words {
			if w = strings.TrimSpace(w); isAllCaps(w) {
				cs = append(cs, w)
			} else {
				ci = append(ci, w)
			}
		}
		d.re, d.wl, d.words, d.names = wordsRegex(ci), newWordList(ci, true), true, true
		if len(cs) > 0 {
			d.reCS, d.wlCS = wordsRegexCase(cs, false), newWordList(cs, false)
		}
	default:
		return nil, fmt.Errorf("builtin, regex ou words requis")
	}
	return d, err
}

func wordsRegex(words []string) *regexp.Regexp { return wordsRegexCase(words, true) }

func wordsRegexCase(words []string, insensitive bool) *regexp.Regexp {
	ws := make([]string, 0, len(words))
	for _, w := range words {
		if w = strings.TrimSpace(w); w != "" {
			ws = append(ws, w)
		}
	}
	if len(ws) == 0 {
		return regexp.MustCompile(`a^`) // never matches
	}
	sort.SliceStable(ws, func(i, j int) bool { return len(ws[i]) > len(ws[j]) })
	for i, w := range ws {
		if isAllCaps(w) && !strings.ContainsAny(w, " .-") { // HUB also as H.U.B. / H.U.B
			var p []string
			for _, r := range w {
				p = append(p, regexp.QuoteMeta(string(r)))
			}
			ws[i] = strings.Join(p, `\.?`) + `\.?`
			if !insensitive {
				ws[i] = `(?:` + regexp.QuoteMeta(w) + `|` + strings.Join(p, `\.`) + `\.?)`
			}
			continue
		}
		ws[i] = strings.ReplaceAll(regexp.QuoteMeta(w), " ", `\s+`)
	}
	flag := ""
	if insensitive {
		flag = "(?i)"
	}
	return regexp.MustCompile(flag + `(?:` + strings.Join(ws, "|") + `)`)
}

// wordList matches a list of terms without scanning the text with the whole alternation:
// the regex is only tried, anchored, where a word starts like one of the terms.
type wordList struct {
	anch   *regexp.Regexp  // the terms that start with a letter or a digit, anchored
	rest   *regexp.Regexp  // the other terms ("+32 470…"), searched in the whole text
	starts map[string]bool // first word of each term (case-folded when fold)
	fold   bool
	asciiB bool // the regex starts with \b (ASCII word boundary) instead of being filtered by boundary()
}

func newWordList(words []string, insensitive bool) *wordList {
	w := &wordList{starts: map[string]bool{}, fold: insensitive}
	var fast, other []string
	for _, t := range words {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		r, n := utf8.DecodeRuneInString(t)
		if !isWordRune(r) {
			other = append(other, t)
			continue
		}
		fast = append(fast, t)
		w.starts[w.key(t[:wordEnd(t, 0)])] = true
		if isAllCaps(t) && !strings.ContainsAny(t, " .-") { // H.U.B.: the first word is "H"
			w.starts[w.key(t[:n])] = true
		}
	}
	if len(fast) > 0 {
		w.anch = regexp.MustCompile(`^` + wordsRegexCase(fast, insensitive).String())
	}
	if len(other) > 0 {
		w.rest = wordsRegexCase(other, insensitive)
	}
	return w
}

// foldMin folds like (?i): the smallest rune of the case-folding orbit.
func foldMin(r rune) rune {
	if r < utf8.RuneSelf {
		if 'a' <= r && r <= 'z' {
			r -= 'a' - 'A'
		}
		if r != 'K' && r != 'S' { // K and S also fold with the Kelvin sign and the long s
			return r
		}
	}
	m := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		m = min(m, f)
	}
	return m
}

func (w *wordList) appendKey(b []byte, t string) []byte {
	if !w.fold {
		return append(b, t...)
	}
	for _, r := range t {
		b = utf8.AppendRune(b, foldMin(r))
	}
	return b
}

func (w *wordList) key(t string) string { return string(w.appendKey(nil, t)) }

func wordEnd(s string, i int) int {
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		if !isWordRune(r) {
			break
		}
		i += n
	}
	return i
}

// find returns the matches of the terms, as the plain regex filtered by boundary() would, plus
// the overlapping ones the regex would have skipped (spans() keeps the longest).
func (w *wordList) find(s string) [][]int {
	var out [][]int
	if w.rest != nil {
		out = w.rest.FindAllStringIndex(s, -1)
	}
	if w.anch == nil {
		return out
	}
	var buf []byte
	prev := ' '
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		start := isWordRune(r) && !isWordRune(prev)
		if w.asciiB {
			start = isASCIIWord(r) && !isASCIIWord(prev)
		}
		prev = r
		if !start {
			i += n
			continue
		}
		j := wordEnd(s, i)
		buf = w.appendKey(buf[:0], s[i:j])
		ok := w.starts[string(buf)]
		if !ok && j > i+n {
			buf = w.appendKey(buf[:0], s[i:i+n])
			ok = w.starts[string(buf)]
		}
		if ok {
			if m := w.anch.FindStringIndex(s[i:]); m != nil {
				out = append(out, []int{i, i + m[1]})
			}
		}
		i += n
	}
	return out
}

func isASCIIWord(r rune) bool {
	return r < utf8.RuneSelf && (r == '_' || '0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z')
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

// strong: evidence good enough to feed the persistent name memory (title, context word, explicit list).
func (d *Detector) strong() bool {
	return d.words || d.builtin == "person_title"
}

// learnKey: how a value is remembered and looked up (lowercase, single spaces).
func learnKey(v string) string { return strings.Join(strings.Fields(strings.ToLower(v)), " ") }

// Find is pure: it may run concurrently on the same text (large texts) and its result is cached.
func (d *Detector) Find(s string) []span {
	var out []span
	if d.custom != nil {
		for _, m := range d.custom(s) {
			out = append(out, span{s: m[0], e: m[1], kind: d.Kind, prio: d.prio, det: d})
		}
		return out
	}
	var ms [][]int
	if d.wl != nil {
		ms = d.wl.find(s)
	} else if d.re != nil {
		ms = d.re.FindAllStringSubmatchIndex(s, -1)
	}
	if d.wlCS != nil {
		ms = append(ms, d.wlCS.find(s)...)
	} else if d.reCS != nil {
		ms = append(ms, d.reCS.FindAllStringSubmatchIndex(s, -1)...)
	}
	for _, m := range ms {
		a, b := m[0], m[1]
		if d.group > 0 && len(m) > 2*d.group+1 && m[2*d.group] >= 0 {
			a, b = m[2*d.group], m[2*d.group+1]
		}
		if a == b || (d.words && !boundary(s, a, b)) || (d.valid != nil && !d.valid(s[a:b])) {
			continue
		}
		if (d.builtin == "niss" || d.builtin == "card") && afterDecimal(s, a) {
			continue
		}
		k := d.Kind
		if d.kinds != nil {
			lk := learnKey(s[a:b])
			if d.capOnly[lk] && !isCap(s[a:b]) {
				continue
			}
			if k = d.kinds[lk]; k == "" { // never emit an empty kind: «_1» could not be restored
				continue
			}
		}
		out = append(out, span{s: a, e: b, kind: k, prio: d.prio, det: d})
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

func atoi(d string) int {
	n := 0
	for i := 0; i < len(d); i++ {
		n = n*10 + int(d[i]-'0')
	}
	return n
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

// afterDecimal: the digits at s[a] are the decimals of a number (0.0012345678), not an identifier.
func afterDecimal(s string, a int) bool {
	return a >= 2 && a < len(s) && s[a-1] == '.' && isDigitByte(s[a-2]) && isDigitByte(s[a])
}

// validNISS: check digits (born before or after 2000) and a plausible birth date
// (month + 20 or + 40 for bis numbers, 00 when unknown).
func validNISS(s string) bool {
	d := onlyDigits(s)
	if len(d) != 11 {
		return false
	}
	if mm, dd := atoi(d[2:4]), atoi(d[4:6]); dd > 31 || !(mm <= 12 || mm >= 20 && mm <= 32 || mm >= 40 && mm <= 52) {
		return false
	}
	n9, cc := atoi(d[:9]), atoi(d[9:])
	return 97-n9%97 == cc || 97-(2000000000+n9)%97 == cc
}

// INAMI / RIZIV number of a healthcare provider: 11 digits (1-23456-25-001), digits 7-8 are a check key
// (m - first six digits mod m, with m = 97, then 89, 83, 79 for newer series; INAMI e-billing
// instructions, annex 16.3), the last 3 the qualification code. The 8-digit form without
// qualification is only accepted right after « INAMI », « RIZIV » or « NIHII », the 11 digits
// written without separators only after a word about a healthcare provider.
var (
	reINAMI      = regexp.MustCompile(`\b[1-9][-./ ]?\d{5}[-./ ]?\d{2}(?:[-./ ]?\d{3})?\b`)
	reINAMICtx   = regexp.MustCompile(`(?i)\b(?:inami|riziv|nihii)\b[^\d\n]{0,25}$`)
	reINAMICtx11 = regexp.MustCompile(`(?i)\b(?:inami|riziv|nihii|prescri(?:pteur|ptrice|ber|ption)|voorschrijver|m[ée]decin|docteur|dokter|arts|dr|zorgverlener|prestataire|praticien|kin[ée]sith[ée]rapeute|infirmi[eè]re?|verpleegkundige|pharmacien|apotheker|dentiste|tandarts)\b[^\d\n]{0,25}$`)
)

func validINAMI(d string) bool {
	if len(d) != 8 && len(d) != 11 {
		return false
	}
	n6, cc := atoi(d[:6]), atoi(d[6:8])
	for _, m := range []int{97, 89, 83, 79} {
		if m-n6%m == cc {
			return true
		}
	}
	return false
}

func findINAMI(s string) [][2]int {
	var out [][2]int
	for _, m := range reINAMI.FindAllStringIndex(s, -1) {
		raw := s[m[0]:m[1]]
		d := onlyDigits(raw)
		if !validINAMI(d) {
			continue
		}
		before := s[max(0, m[0]-40):m[0]]
		if len(d) == 8 && !reINAMICtx.MatchString(before) || len(raw) == 11 && !reINAMICtx11.MatchString(before) || afterDecimal(s, m[0]) {
			continue
		}
		out = append(out, [2]int{m[0], m[1]})
	}
	return out
}

// a number never spans a line break: separators are horizontal spaces, dots, slashes and dashes
var rePhone = regexp.MustCompile(`(?:\+|\b00)[1-9]\d{1,2}[\t \x{00A0}./-]?(?:\(0\)[\t \x{00A0}./-]?)?\d(?:[\t \x{00A0}./-]?\d){6,10}\b|\b0[1-9](?:[\t \x{00A0}./-]?\d){7,8}\b`)

// slashOK: a slash only follows the zone or mobile prefix ("02/541 91 91", "0470/12 34 56");
// "08/10/2026 14" is a date, "001435/001442" two serial numbers.
func slashOK(v string) bool {
	i := strings.IndexByte(v, '/')
	if i < 0 {
		return true
	}
	prefix := 4 // 0 + zone or mobile prefix
	if v[0] == '+' || strings.HasPrefix(v, "00") {
		prefix = 7 // + country code ("+32 470/12 34 56")
	}
	return strings.Count(v, "/") == 1 && len(onlyDigits(v[:i])) <= prefix && !strings.HasPrefix(v[i+1:], "0")
}

func findPhones(s string) [][2]int {
	var out [][2]int
	for _, m := range rePhone.FindAllStringIndex(s, -1) {
		if n := len(onlyDigits(s[m[0]:m[1]])); n < 8 || n > 15 || !slashOK(s[m[0]:m[1]]) {
			continue
		}
		if afterDecimal(s, m[0]) { // 0.0012345678: a decimal number
			continue
		}
		out = append(out, [2]int{m[0], m[1]})
	}
	return out
}

// IBAN countries (registry and experimental): a lowercase IBAN must start with one of them.
var ibanCountries = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range strings.Fields(`AD AE AL AT AZ BA BE BG BH BI BR BY CH CR CY CZ DE DJ DK DO EE EG ES FI FK FO FR GB GE GI GL GR
		GT HN HR HU IE IL IQ IS IT JO KW KZ LB LC LI LT LU LV LY MC MD ME MK MN MR MT MU NI NL NO OM PK PL PS PT QA RO RS RU SA SC
		SD SE SI SK SM SO ST SV TL TN TR UA VA VG XK YE DZ AO BJ BF CM CV CF TD KM CG GQ GA GW IR CI MG ML MA MZ NE SN TG`) {
		m[c] = true
	}
	return m
}()

var reIBAN = regexp.MustCompile(`(?i)\b[A-Z]{2}\d{2}(?:[ .\-]?[A-Z0-9]{4}){2,7}(?:[ .\-]?[A-Z0-9]{1,3})?\b`)

// findIBAN: the regex is case-insensitive, so it may take the next word as a last group
// ("BE68 5390 0754 7034 svp"): trailing groups are dropped until the check digits match.
func findIBAN(s string) [][2]int {
	var out [][2]int
	for _, m := range reIBAN.FindAllStringIndex(s, -1) {
		for a, b := m[0], m[1]; ; {
			if validIBAN(s[a:b]) {
				out = append(out, [2]int{a, b})
				break
			}
			k := strings.LastIndexAny(s[a:b], " .-")
			if k <= 4 {
				break
			}
			b = a + k
		}
	}
	return out
}

// validIBAN: mod-97 check, separators (space, dot, dash) and case ignored.
func validIBAN(s string) bool {
	lower := strings.ToUpper(s) != s
	s = strings.ToUpper(strings.NewReplacer(" ", "", ".", "", "-", "").Replace(s))
	if len(s) < 15 || len(s) > 34 || lower && !ibanCountries[s[:2]] {
		return false
	}
	rem := 0
	for _, r := range s[4:] + s[:4] {
		switch {
		case r >= '0' && r <= '9':
			rem = (rem*10 + int(r-'0')) % 97
		case r >= 'A' && r <= 'Z':
			rem = (rem*100 + int(r-'A'+10)) % 97
		default:
			return false
		}
	}
	return rem == 1
}

func luhn(d string) bool {
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

// validCard: Luhn, issuer prefix and length (Amex 15; Visa, Mastercard, Maestro… 16; Maestro and
// Bancontact up to 19). 13- and 14-digit numbers (millisecond timestamps) are not taken for cards.
func validCard(s string) bool {
	d := onlyDigits(s)
	if len(d) < 15 || len(d) > 19 || !luhn(d) {
		return false
	}
	switch len(d) {
	case 15:
		return d[:2] == "34" || d[:2] == "37"
	case 16:
		p := atoi(d[:4])
		return d[0] >= '3' && d[0] <= '6' || p >= 2221 && p <= 2720
	case 17, 18:
		return d[0] == '5' || d[0] == '6'
	}
	return d[0] >= '3' && d[0] <= '6'
}

// ---------- normalized view of a text ----------

// textView: the text as the detectors see it. Format characters (soft hyphen, zero-width
// spaces…) are removed, Unicode spaces become ' ', fullwidth ASCII becomes ASCII and JSON
// escapes (\n, \", \u00e9…) are decoded; pos and end map each byte of s back to the original
// text, so that the original span is the one replaced.
type textView struct {
	s        string
	pos, end []int32 // nil: s is the original text
}

func (v *textView) orig(a, b int) (int, int) {
	if v.pos == nil {
		return a, b
	}
	return int(v.pos[a]), int(v.end[b-1])
}

// viewRune: how the detectors see a non-ASCII rune; false: the rune is dropped.
func viewRune(r rune) (rune, bool) {
	if r < 0x2000 && r != 0x85 && r != 0xA0 && r != 0xAD && r != 0x1680 && (r < 0x600 || r > 0x180E) {
		return r, true
	}
	switch {
	case r == 0x85 || r == 0x2028 || r == 0x2029:
		return '\n', true
	case r >= 0xFF01 && r <= 0xFF5E: // fullwidth ASCII
		return r - 0xFEE0, true
	case r >= 0x2010 && r <= 0x2012: // hyphen, non-breaking hyphen, figure dash
		return '-', true
	case unicode.Is(unicode.Cf, r):
		return 0, false
	case unicode.IsSpace(r):
		return ' ', true
	}
	return r, true
}

func hex4(t string, i int) (rune, bool) {
	if i+4 > len(t) {
		return 0, false
	}
	var r rune
	for _, c := range []byte(t[i : i+4]) {
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return r, true
}

// unescapeAt decodes the escape at t[i] ('\\'): a run of backslashes (one per level of JSON
// encoding) and the escaped character. n is the length consumed, 0 when this is no escape;
// run is the number of backslashes.
func unescapeAt(t string, i int) (r rune, n, run int) {
	j := i
	for j < len(t) && t[j] == '\\' {
		j++
	}
	run = j - i
	if j >= len(t) {
		return 0, 0, run
	}
	switch c := t[j]; c {
	case 'n':
		return '\n', run + 1, run
	case 'r':
		return '\r', run + 1, run
	case 't':
		return '\t', run + 1, run
	case 'b', 'f':
		return ' ', run + 1, run
	case '"', '\'', '/':
		return rune(c), run + 1, run
	case 'u':
		h, ok := hex4(t, j+1)
		if !ok {
			return 0, 0, run
		}
		n = run + 5
		if !utf16.IsSurrogate(h) {
			return h, n, run
		}
		k := i + n
		m := k
		for m < len(t) && t[m] == '\\' {
			m++
		}
		if m > k && m < len(t) && t[m] == 'u' {
			if lo, ok := hex4(t, m+1); ok {
				if r := utf16.DecodeRune(h, lo); r != unicode.ReplacementChar {
					return r, m + 5 - i, run
				}
			}
		}
		return unicode.ReplacementChar, n, run
	}
	return 0, 0, run
}

func isEscapeChar(c byte) bool { return strings.IndexByte(`nrtbf"'/u\`, c) >= 0 }

func needsView(t string) bool {
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c == '\\' && i+1 < len(t) && isEscapeChar(t[i+1]) {
			return true
		}
		if c >= utf8.RuneSelf {
			r, n := utf8.DecodeRuneInString(t[i:])
			if v, keep := viewRune(r); !keep || v != r {
				return true
			}
			i += n - 1
		}
	}
	return false
}

func newView(t string) textView {
	if !needsView(t) {
		return textView{s: t}
	}
	b := make([]byte, 0, len(t))
	pos := make([]int32, 0, len(t))
	end := make([]int32, 0, len(t))
	put := func(r rune, a, e int) {
		n0 := len(b)
		b = utf8.AppendRune(b, r)
		for k := n0; k < len(b); k++ {
			pos, end = append(pos, int32(a)), append(end, int32(e))
		}
	}
	copyRaw := func(a, e int) { // bytes kept as they are; a multi-byte rune maps to itself
		for k := a; k < e; k++ {
			b, pos, end = append(b, t[k]), append(pos, int32(a)), append(end, int32(e))
		}
	}
	for i := 0; i < len(t); {
		c := t[i]
		if c == '\\' {
			r, n, run := unescapeAt(t, i)
			if n == 0 {
				for k := i; k < i+run; k++ {
					copyRaw(k, k+1)
				}
				i += run
				continue
			}
			if v, keep := viewRune(r); keep {
				put(v, i, i+n)
			}
			i += n
			continue
		}
		if c < utf8.RuneSelf {
			copyRaw(i, i+1)
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(t[i:])
		if v, keep := viewRune(r); !keep {
		} else if v == r {
			copyRaw(i, i+n)
		} else {
			put(v, i, i+n)
		}
		i += n
	}
	return textView{s: string(b), pos: pos, end: end}
}

// ---------- JSON documents in a text (tool results, tool arguments) ----------

// jsonLit: a string or number token of a JSON document (byte offsets, quotes included).
type jsonLit struct {
	s, e int
	str  bool
	skip bool // value of an "encrypted…" key: never modified
}

// jsonDoc reports whether t is a JSON object or array.
func jsonDoc(t string) bool {
	t = strings.TrimSpace(t)
	if len(t) < 2 || !(t[0] == '{' && t[len(t)-1] == '}' || t[0] == '[' && t[len(t)-1] == ']') {
		return false
	}
	return json.Valid([]byte(t))
}

// jsonLits lists the string and number tokens of a valid JSON document.
func jsonLits(t string) []jsonLit {
	type frame struct {
		obj, wantKey, skip bool
		key                string
	}
	var st []frame
	var out []jsonLit
	valueSkip := func() bool {
		if len(st) == 0 {
			return false
		}
		f := st[len(st)-1]
		return f.skip || f.obj && strings.HasPrefix(f.key, "encrypted")
	}
	for i := 0; i < len(t); {
		switch c := t[i]; {
		case c == '{' || c == '[':
			st = append(st, frame{obj: c == '{', wantKey: c == '{', skip: valueSkip()})
			i++
		case c == '}' || c == ']':
			if len(st) > 0 {
				st = st[:len(st)-1]
			}
			i++
		case c == ',':
			if len(st) > 0 && st[len(st)-1].obj {
				st[len(st)-1].wantKey = true
			}
			i++
		case c == '"':
			j := i + 1
			for j < len(t) && t[j] != '"' {
				if t[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(t))
			if f := len(st) - 1; f >= 0 && st[f].obj && st[f].wantKey {
				st[f].key, st[f].wantKey = t[i+1:max(i+1, j-1)], false
				out = append(out, jsonLit{i, j, true, st[f].skip})
			} else {
				out = append(out, jsonLit{i, j, true, valueSkip()})
			}
			i = j
		case c == '-' || isDigitByte(c):
			j := i + 1
			for j < len(t) && strings.IndexByte("0123456789.eE+-", t[j]) >= 0 {
				j++
			}
			out = append(out, jsonLit{i, j, false, valueSkip()})
			i = j
		default:
			i++
		}
	}
	return out
}

// litAt: the token that contains [a, b), if any.
func litAt(ls []jsonLit, a, b int) (jsonLit, bool) {
	k := sort.Search(len(ls), func(i int) bool { return ls[i].e > a })
	if k < len(ls) && ls[k].s <= a && b <= ls[k].e {
		return ls[k], true
	}
	return jsonLit{}, false
}

// ---------- per-request session: anonymize in, restore out ----------

type Session struct {
	cfg          *Config
	prof         *Profile
	dets         []*Detector
	learned      *Detector
	learn        map[string]string // learnKey(value) -> kind
	capOnly      map[string]bool   // learned values that only match when capitalized (name words, places)
	surface      map[string]string // learned name word -> its form in the text ("Dupont")
	fwd          map[string]string // kind \x00 learnKey(value) -> placeholder id
	rev          map[string]string // placeholder id -> original value
	cnt          map[string]int
	fuzzy        map[string]string // one-deletion variant of a learned name word -> lower(word)
	allow        map[string]bool
	nameKinds    map[string]bool      // kinds whose detectors find names
	found        map[string]*detected // detector spans of each text, found in pass 1 and reused in pass 2
	active       []*Detector
	cache        *spanCache
	toLearn      []string
	learnOn      bool // a person_learned detector is configured
	NoLearn      bool
	Stats        map[string]map[string]int // kind -> action -> count
	Debug        bool                      // record every detected value (debug archive)
	BlockedFiles []string                  // names of the files refused by the profile
	AlertFiles   []string                  // names of the refused files that trigger an email alert
	FileLog      []FileEvent               // files and images to record in the file log (30 days)
	api          string
	cur          string // part of the request being processed (debug)
	Events       map[string]*DebugEvent
	Anon         int
	Blocked      int
}

func NewSession(cfg *Config, prof *Profile) *Session {
	s := &Session{cfg: cfg, prof: prof, dets: cfg.dets, allow: cfg.allow, capOnly: map[string]bool{}, surface: map[string]string{},
		learn: map[string]string{}, fwd: map[string]string{}, rev: map[string]string{}, cnt: map[string]int{}, fuzzy: map[string]string{},
		nameKinds: map[string]bool{}, found: map[string]*detected{}, cache: detCache, Stats: map[string]map[string]int{}}
	for _, d := range cfg.dets {
		s.learnOn = s.learnOn || d.builtin == "person_learned"
		if d.names {
			s.nameKinds[d.Kind] = true
		}
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

// DebugEvent: one detected value and what was done with it (debug mode only).
type DebugEvent struct {
	Kind, Action, Value, Placeholder string
	N                                int
	Sources                          map[string]int // where in the request: "system", "user@1" (1 = last message)…
}

func (s *Session) event(kind, act, v, ph string) {
	if !s.Debug {
		return
	}
	if s.Events == nil {
		s.Events = map[string]*DebugEvent{}
	}
	k := kind + "\x00" + act + "\x00" + v
	e := s.Events[k]
	if e == nil {
		e = &DebugEvent{kind, act, v, strings.Trim(ph, "«»"), 0, map[string]int{}}
		s.Events[k] = e
	}
	e.N++
	if s.cur != "" {
		e.Sources[s.cur]++
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

// detected: what the configured detectors found in a text (view offsets).
type detected struct {
	tv   textView
	sps  []span
	json []jsonLit // tokens of the JSON document the text holds, if any
}

// parallelMin: texts from this size have their detectors run concurrently, on at most
// detectWorkers goroutines (0: GOMAXPROCS).
const parallelMin = 16 << 10

var detectWorkers = 0

// detect runs the configured detectors once per text (the second pass only adds the learned
// values), reusing the spans cached by earlier requests for the detectors that only depend on
// the text and the configuration.
func (s *Session) detect(text string) *detected {
	if d, ok := s.found[text]; ok {
		return d
	}
	tv := newView(text)
	if s.active == nil { // the detectors the profile does not turn off
		s.active = []*Detector{}
		for _, d := range s.dets {
			if s.action(d.Kind) != "off" {
				s.active = append(s.active, d)
			}
		}
	}
	dets := s.active
	res := make([][]span, len(dets))
	var key cacheKey
	var ent *cacheEntry
	cached := s.cache != nil && len(text) >= cacheMinLen
	if cached {
		key = cacheKey{sha256.Sum256([]byte(text)), s.cfg}
		ent = s.cache.get(key)
	}
	var todo []int
	fresh := false // spans worth caching were computed
	for i, d := range dets {
		if ent != nil && !d.volatile() {
			if sp, ok := ent.lookup(d); ok {
				res[i] = sp
				continue
			}
		}
		todo, fresh = append(todo, i), fresh || !d.volatile()
	}
	findAll(tv.s, dets, todo, res)
	if cached && fresh {
		s.cache.add(key, ent, dets, todo, res)
	}
	out := &detected{tv: tv}
	for _, r := range res {
		out.sps = append(out.sps, r...)
	}
	if jsonDoc(text) {
		out.json = jsonLits(text)
	}
	s.found[text] = out
	return out
}

// findAll runs the detectors listed in todo, concurrently on large texts; res keeps the
// detector order, so the result does not depend on scheduling.
func findAll(text string, dets []*Detector, todo []int, res [][]span) {
	n := runtime.GOMAXPROCS(0)
	if detectWorkers > 0 {
		n = detectWorkers
	}
	n = min(n, len(todo))
	if len(text) < parallelMin || n < 2 {
		for _, i := range todo {
			res[i] = dets[i].Find(text)
		}
		return
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var panicked any
	next := make(chan int, len(todo))
	for _, i := range todo {
		next <- i
	}
	close(next)
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { // re-raised in the request's goroutine, where net/http recovers it
				if p := recover(); p != nil {
					mu.Lock()
					panicked = p
					mu.Unlock()
				}
			}()
			for i := range next {
				res[i] = dets[i].Find(text)
			}
		}()
	}
	wg.Wait()
	if panicked != nil {
		panic(panicked)
	}
}

// find: the values to act on in text (original offsets), overlaps resolved.
func (s *Session) find(text string, withLearned bool) (*detected, []span) {
	d := s.detect(text)
	all := append([]span(nil), d.sps...)
	if withLearned {
		all = s.addLearned(d.tv.s, all)
	}
	return d, s.resolve(d, all)
}

func (s *Session) spans(text string, withLearned bool) []span {
	_, sps := s.find(text, withLearned)
	return sps
}

// addLearned: values learned in pass 1, and typos of the names among them.
func (s *Session) addLearned(v string, all []span) []span {
	if s.learned != nil {
		all = append(all, s.learned.Find(v)...)
	}
	if len(s.fuzzy) == 0 {
		return all
	}
	for _, t := range tokenize(v) {
		if !isCap(t.w) || utf8.RuneCountInString(t.w) < 5 {
			continue
		}
		f := fold(t.w)
		if s.allow[f] || commonWord(t.w) { // "Routers" is not a typo of Wouters
			continue
		}
		for _, vr := range variants(f) {
			if c, ok := s.fuzzy[vr]; ok && typoOf(f, fold(c)) {
				all = append(all, span{s: t.s, e: t.e, kind: s.learn[c], prio: len(s.dets) + 1, canon: c})
				break
			}
		}
	}
	return all
}

// typoOf: a is b with one typo: a letter missing or added, two letters swapped, or, in long
// names only, one letter replaced (Dupont and Dumont are two names).
func typoOf(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) != len(rb) {
		return editWithin1(a, b)
	}
	var diff []int
	for i := range ra {
		if ra[i] != rb[i] {
			diff = append(diff, i)
		}
	}
	switch len(diff) {
	case 0:
		return true
	case 1:
		return len(ra) >= 7
	case 2:
		return diff[1] == diff[0]+1 && ra[diff[0]] == rb[diff[1]] && ra[diff[1]] == rb[diff[0]]
	}
	return false
}

// severity: on overlapping detections, the strictest action wins (an IBAN set to block is never
// hidden by a reference number set to log).
var severity = map[string]int{"block": 3, "anonymize": 2, "log": 1}

func (s *Session) resolve(d *detected, all []span) []span {
	tv := &d.tv
	rank := map[string]int{}
	cand := all[:0]
	for _, sp := range all {
		if sp.kind == "" || sp.e <= sp.s || s.allowed(tv.s[sp.s:sp.e], sp.kind) {
			continue
		}
		if d.json != nil { // encrypted blobs of a JSON document are never touched
			a, b := tv.orig(sp.s, sp.e)
			if l, ok := litAt(d.json, a, b); ok && l.skip {
				continue
			}
		}
		if _, ok := rank[sp.kind]; !ok {
			rank[sp.kind] = severity[s.action(sp.kind)]
		}
		cand = append(cand, sp)
	}
	if len(cand) == 0 {
		return nil
	}
	sort.SliceStable(cand, func(i, j int) bool {
		a, b := cand[i], cand[j]
		if ra, rb := rank[a.kind], rank[b.kind]; ra != rb {
			return ra > rb
		}
		if a.s != b.s {
			return a.s < b.s
		}
		if a.e-a.s != b.e-b.s {
			return a.e-a.s > b.e-b.s
		}
		return a.prio < b.prio
	})
	// per severity level, from the strictest: the earliest and longest spans that overlap
	// neither the previous ones of the level nor the stricter ones already kept
	var acc []span // sorted by start, non-overlapping
	for lo := 0; lo < len(cand); {
		hi := lo
		for hi < len(cand) && rank[cand[hi].kind] == rank[cand[lo].kind] {
			hi++
		}
		var cur []span
		end := 0
		for _, sp := range cand[lo:hi] {
			if sp.s < end {
				continue
			}
			if k := sort.Search(len(acc), func(i int) bool { return acc[i].e > sp.s }); k < len(acc) && acc[k].s < sp.e {
				continue
			}
			cur, end = append(cur, sp), sp.e
		}
		if len(acc) == 0 {
			acc = cur
		} else if len(cur) > 0 {
			acc = append(acc, cur...)
			sort.Slice(acc, func(i, j int) bool { return acc[i].s < acc[j].s })
		}
		lo = hi
	}
	for i, sp := range acc {
		sp.v = tv.s[sp.s:sp.e]
		sp.s, sp.e = tv.orig(sp.s, sp.e)
		acc[i] = sp
	}
	return acc
}

// allowed: the value is on the allow list, or it is a name made of an allowed first word and
// words that are allowed or common ("Claude Code", "Claude Cowork"), not "Claude Dubois".
func (s *Session) allowed(v, kind string) bool {
	if len(s.allow) == 0 {
		return false
	}
	if s.allow[fold(strings.Join(strings.Fields(v), " "))] {
		return true
	}
	if !s.nameKinds[kind] {
		return false
	}
	ts := tokenize(v)
	if len(ts) == 0 || !s.allow[fold(ts[0].w)] {
		return false
	}
	for _, t := range ts[1:] {
		if f := fold(t.w); !s.allow[f] && !particles[f] && !commonWord(t.w) {
			return false
		}
	}
	return true
}

// learned words lose the punctuation around them ("Dupont." -> "Dupont")
const trimPunct = ".,;:!?()[]{}\"«»“”‘'’*_`"

// learnFrom (pass 1) remembers every value to anonymize, so that a name found
// once ("Mme Dupont") is also replaced where it appears alone ("Dupont").
func (s *Session) learnFrom(text string) string {
	for _, sp := range s.spans(text, false) {
		if s.action(sp.kind) != "anonymize" {
			continue
		}
		v := sp.v
		if isAllCaps(v) && sp.det != nil && sp.det.words && sp.det.kinds == nil { // list acronym: must not be learned case-insensitively
			continue
		}
		if !learnable(v) {
			continue
		}
		k := learnKey(v)
		s.learn[k] = sp.kind
		if isCap(v) {
			s.capOnly[k] = true // learned capitalized, found again capitalized only: "Max", not "max"
		}
		if !s.nameKinds[sp.kind] {
			continue
		}
		for _, t := range strings.FieldsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || r == '-' }) {
			t = strings.Trim(t, trimPunct)
			if r, _ := utf8.DecodeRuneInString(t); utf8.RuneCountInString(t) >= 3 && unicode.IsUpper(r) && !particles[fold(t)] && !commonWord(t) {
				lt := strings.ToLower(t)
				if _, ok := s.learn[lt]; !ok {
					s.learn[lt] = sp.kind
					s.capOnly[lt] = true
				}
				if _, ok := s.surface[lt]; !ok {
					s.surface[lt] = t
				}
				if utf8.RuneCountInString(t) >= 6 { // typos are only looked for in names of 6 letters or more
					for _, vr := range variants(fold(t)) {
						if len(vr) >= 4 {
							s.fuzzy[vr] = lt
						}
					}
				}
			}
		}
		if sp.det != nil && sp.det.strong() && sp.det.kinds == nil { // guesses from a first name are not persisted
			s.toLearn = append(s.toLearn, nameTokens(v)...)
		}
	}
	return text
}

// learnable: a value worth looking for elsewhere in the conversation. An ordinary word, a word
// of one or two letters or a short acronym found once by a heuristic ("The" after "chat",
// "DJI", "Kit") must not be replaced everywhere, turn after turn.
func learnable(v string) bool {
	words := strings.FieldsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || r == '-' })
	if len(words) == 0 {
		return false
	}
	if strings.Contains(v, "@") || len(onlyDigits(v)) >= 5 { // emails, phone numbers, identifiers: exact values
		return true
	}
	if strings.ContainsAny(v, "0123456789") { // a short number and a word ("6148 Aug", "Clos le 20"): the detector
		return false // finds it again in its own layout, a guess must not spread to every turn
	}
	for _, w := range words {
		w = strings.Trim(w, trimPunct)
		if utf8.RuneCountInString(w) >= 3 && !commonWord(w) && !(isAllCaps(w) && utf8.RuneCountInString(w) <= 4) {
			return true // at least one word that is really a name
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
	sort.Strings(ws)
	s.learned = &Detector{wl: newWordList(ws, true), words: true, kinds: s.learn, capOnly: s.capOnly, prio: len(s.dets)}
}

var reDottedAcronym = regexp.MustCompile(`^(?:\p{Lu}\.)+\p{Lu}?\.?$`)

// placeholder: the token of a value (v as detected; surface: the form restored in the answer).
func (s *Session) placeholder(kind, v, surface string) string {
	key := learnKey(v)
	if reDottedAcronym.MatchString(v) { // H.U.B. and HUB: same entity, same token
		key = strings.ToLower(strings.ReplaceAll(v, ".", ""))
	}
	k := kind + "\x00" + key
	id, ok := s.fwd[k]
	if !ok {
		s.cnt[kind]++
		id = fmt.Sprintf("%s_%d", kind, s.cnt[kind])
		s.fwd[k], s.rev[id] = id, surface
	}
	return "«" + id + "»"
}

// token: the placeholder of a detected span of text.
func (s *Session) token(sp span, orig string) string {
	v, surface := sp.v, orig
	if strings.Contains(orig, `\`) { // JSON-escaped in the request: the answer gets the decoded value
		surface = sp.v
	}
	if sp.canon != "" {
		v, surface = sp.canon, s.surface[sp.canon]
		if surface == "" {
			surface = sp.canon
		}
	}
	return s.placeholder(sp.kind, v, surface)
}

// Anonymize (pass 2) applies the profile's action to every detected value.
func (s *Session) Anonymize(text string) string {
	d, sps := s.find(text, true)
	if len(sps) == 0 {
		return text
	}
	var b strings.Builder
	last, qEnd, changed := 0, -1, false
	closeQuote := func(upto int) { // end of a JSON number turned into a string
		if qEnd >= 0 && upto >= qEnd {
			b.WriteString(text[last:qEnd])
			b.WriteByte('"')
			last, qEnd = qEnd, -1
		}
	}
	for _, sp := range sps {
		closeQuote(sp.s)
		act := s.action(sp.kind)
		s.count(sp.kind, act)
		orig := text[sp.s:sp.e]
		if act != "anonymize" {
			s.event(sp.kind, act, orig, "")
			continue
		}
		ph := s.token(sp, orig)
		s.event(sp.kind, act, orig, ph)
		if d.json != nil && qEnd < 0 { // {"niss": 90010112395} -> {"niss": "«NISS_1»"}
			if l, ok := litAt(d.json, sp.s, sp.e); ok && !l.str {
				b.WriteString(text[last:l.s])
				b.WriteByte('"')
				last, qEnd = l.s, l.e
			}
		}
		b.WriteString(text[last:sp.s])
		b.WriteString(ph)
		last, changed = sp.e, true
	}
	if !changed {
		return text
	}
	closeQuote(len(text))
	b.WriteString(text[last:])
	return b.String()
}

var phRe = regexp.MustCompile(`«[\s\x{00A0}\x{202F}]?([A-Z][A-Z0-9_]*_\d+)[\s\x{00A0}\x{202F}]?»`)

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

// RestoreJSON is Restore for JSON text (tool arguments): the values are inserted JSON-escaped.
func (s *Session) RestoreJSON(t string) string { return jsonSafe(s.Restore)(t) }

// jsonSafe turns a restore function into one for JSON text: each restored value is JSON-escaped
// (without its quotes), so that a tab, a quote or a newline in it keeps the JSON valid.
func jsonSafe(fn func(string) string) func(string) string {
	return func(t string) string {
		if !strings.Contains(t, "«") {
			return t
		}
		return phRe.ReplaceAllStringFunc(t, func(m string) string {
			if r := fn(m); r != m {
				return jsonEscape(r)
			}
			return m
		})
	}
}

// jsonEscape: the JSON string encoding of v, without the quotes.
func jsonEscape(v string) string {
	b := appendJSONString(nil, v)
	return string(b[1 : len(b)-1])
}

func appendJSONString(b []byte, s string) []byte {
	const hexd = "0123456789abcdef"
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			b = append(b, s[start:i]...)
			switch c {
			case '"', '\\':
				b = append(b, '\\', c)
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				b = append(b, '\\', 'u', '0', '0', hexd[c>>4], hexd[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			b = append(b, s[start:i]...)
			b = append(b, `\ufffd`...)
			i++
			start = i
			continue
		}
		if r == 0x2028 || r == 0x2029 {
			b = append(b, s[start:i]...)
			b = append(b, '\\', 'u', '2', '0', '2', hexd[r&0xF])
			i += n
			start = i
			continue
		}
		i += n
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}

// no concrete example (e.g. PERSON_1): it would collide with the real tokens of the request
const hintText = "Note : certaines données ont été pseudonymisées et remplacées par des jetons de la forme «TYPE_numéro». Reprends ces jetons exactement tels quels, guillemets « » compris, sans les traduire, les modifier ni les commenter."

// Process anonymizes a request body in place (both API formats).
func (s *Session) Process(body map[string]any, api string) {
	s.api = api
	s.media(body)
	s.walkBody(body, s.docMarkers, false)
	s.walkBody(body, s.learnFrom, false)
	s.buildLearned()
	s.walkBody(body, s.Anonymize, s.Debug)
	s.maskFileNames()
	if s.prof.Hint { // always, not only when something was replaced: the prompt prefix stays the same turn to turn
		injectHint(body, api)
	}
	if p := strings.TrimSpace(s.prof.SystemPrompt); p != "" { // after anonymization: the admin's text is sent as is
		injectSystem(body, api, p)
	}
	s.commitLearning()
}

// commitLearning feeds the persistent (hashed) name memory.
func (s *Session) commitLearning() {
	if s.learnOn && !s.NoLearn {
		learner.Learn(s.toLearn)
	}
}

// bodyOrder: the request parts walked first, in this order. Placeholders are numbered in walk
// order, so tools and system prompt come before the conversation: a new value in the last
// message never renumbers the prefix the provider keeps in its prompt cache.
var bodyOrder = []string{"tools", "functions", "system", "instructions", "prompt", "messages", "input"}

// speakerRoles: messages whose "name" is the name of a participant (not of a function).
var speakerRoles = map[string]bool{"user": true, "assistant": true, "system": true, "developer": true}

// walkBody applies fn to the user-content strings of a request, in a stable order.
// parts: record the part being processed (message role and position) for the debug view.
func (s *Session) walkBody(body map[string]any, fn func(string) string, parts bool) {
	keys := make([]string, 0, len(body))
	for _, k := range bodyOrder {
		if _, ok := body[k]; ok {
			keys = append(keys, k)
		}
	}
	for _, k := range sortedKeys(body) {
		if !slices.Contains(bodyOrder, k) {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		if parts {
			s.cur = k
		}
		switch k {
		case "tools", "functions":
			body[k] = walk(body[k], modeSchema, fn)
		case "messages":
			msgs, ok := body[k].([]any)
			if !ok {
				body[k] = walk(body[k], modeStruct, fn)
				continue
			}
			for i, m := range msgs {
				mm, _ := m.(map[string]any)
				role, _ := mm["role"].(string)
				if parts {
					r := role
					if r == "" {
						r = "?"
					}
					s.cur = fmt.Sprintf("%s@%d", r, len(msgs)-i) // counted from the end: stable when messages are prepended
				}
				if n, ok := mm["name"].(string); ok && speakerRoles[role] {
					mm["name"] = walkName(n, fn)
				}
				msgs[i] = walk(m, modeStruct, fn)
				// the reasoning of an earlier answer (DeepSeek, GLM, Grok, vLLM), sent back by the
				// client as it was restored: unsigned text, pseudonymized like the content
				for _, f := range reasoningKeys {
					if t, ok := mm[f].(string); ok {
						mm[f] = fn(t)
					}
				}
			}
		case "user", "safety_identifier", "stop", "stop_sequences": // OpenAI end-user id, stop sequences
			body[k] = walk(body[k], modeText, fn)
		default:
			w := map[string]any{k: body[k]}
			walk(w, modeStruct, fn)
			body[k] = w[k]
		}
	}
	if parts {
		s.cur = ""
	}
}

// walkName: the "name" of an OpenAI message ("Claire_Dubois") is scanned with its underscores as
// spaces; once pseudonymized it keeps the pattern ^[a-zA-Z0-9_-]+$ ("PERSON_1").
func walkName(n string, fn func(string) string) string {
	t := strings.ReplaceAll(n, "_", " ")
	r := fn(t)
	if r == t {
		return n
	}
	return strings.Map(func(c rune) rune {
		switch {
		case c == '«' || c == '»':
			return -1
		case c == '-' || isASCIIWord(c):
			return c
		}
		return '_'
	}, r)
}

// AnonymizeText is Process for a bare string (dashboard test panel).
func (s *Session) AnonymizeText(t string) string {
	s.learnFrom(t)
	s.buildLearned()
	return s.Anonymize(t)
}

// injectSystem puts the profile's behaviour prompt first, before the client's own instructions.
func injectSystem(body map[string]any, api, text string) {
	if api == "anthropic" {
		switch sys := body["system"].(type) {
		case string:
			body["system"] = text + "\n\n" + sys
		case []any:
			body["system"] = append([]any{map[string]any{"type": "text", "text": text}}, sys...)
		default:
			body["system"] = text
		}
		return
	}
	msgs, _ := body["messages"].([]any)
	body["messages"] = append([]any{map[string]any{"role": "system", "content": text}}, msgs...)
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

// ---------- files: blocking by extension ----------

func normExt(e string) string {
	e = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(e), ".")))
	if e == "jpeg" {
		e = "jpg"
	}
	return e
}

var mimeExt = map[string]string{"application/pdf": "pdf", "image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp",
	"image/heic": "heic", "application/zip": "zip", "application/x-zip-compressed": "zip", "application/x-7z-compressed": "7z",
	"application/vnd.rar": "rar", "application/x-tar": "tar", "application/gzip": "gz", "application/msword": "doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "docx", "application/vnd.ms-excel": "xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": "xlsx", "application/vnd.ms-powerpoint": "ppt",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx", "text/plain": "txt", "text/csv": "csv",
	"text/markdown": "md", "application/json": "json", "text/html": "html", "application/rtf": "rtf",
	"application/vnd.oasis.opendocument.text": "odt", "application/vnd.oasis.opendocument.spreadsheet": "ods"}

// fileOf returns a display name and the extension of a file or image content part.
func fileOf(m map[string]any, typ string) (string, string) {
	var name, mime string
	look := func(x map[string]any) {
		for _, k := range []string{"gw_name", "filename", "file_name", "name", "title"} {
			if v, _ := x[k].(string); v != "" && name == "" {
				name = v
			}
		}
		for _, k := range []string{"media_type", "mime_type"} {
			if v, _ := x[k].(string); v != "" && mime == "" {
				mime = v
			}
		}
		for _, k := range []string{"url", "file_data", "data"} {
			if v, _ := x[k].(string); strings.HasPrefix(v, "data:") && mime == "" {
				mime = strings.SplitN(strings.TrimPrefix(v, "data:"), ";", 2)[0]
			} else if k == "url" && v != "" && name == "" && !strings.HasPrefix(v, "data:") {
				if i := strings.LastIndexAny(strings.SplitN(v, "?", 2)[0], "/"); i >= 0 {
					name = strings.SplitN(v, "?", 2)[0][i+1:]
				}
			}
		}
	}
	look(m)
	for _, k := range []string{typ, "source", "file", "image_url"} {
		if sub, ok := m[k].(map[string]any); ok {
			look(sub)
		}
	}
	ext := ""
	if i := strings.LastIndex(name, "."); i >= 0 && i < len(name)-1 {
		ext = normExt(name[i+1:])
	}
	if ext == "" {
		ext = mimeExt[strings.ToLower(mime)]
	}
	if name == "" && ext != "" {
		name = "fichier ." + ext
	}
	return name, ext
}

// FileEvent: one file or image seen in a request, as written to the file log (never its content).
type FileEvent struct {
	Kind, Name, Ext, SHA, Action string
	Size                         int
}

// fileAction: what the profile does with a file of this extension. An extension listed in
// files wins; otherwise images follow the image policy and the other files files_default.
func (s *Session) fileAction(kind, ext string) string {
	if a, ok := s.prof.Files[ext]; ok && ext != "" {
		return a
	}
	if kind == "IMAGE" || imageExts[ext] {
		return s.prof.Images // pass | blank | block
	}
	return s.prof.FilesDefault
}

var imageExts = map[string]bool{"png": true, "jpg": true, "gif": true, "webp": true, "heic": true}

var statAction = map[string]string{"pass": "pass", "pass_log": "pass", "blank": "strip", "block": "block", "block_alert": "block"}

// file applies the action to one file: counters, debug event, file log, block list.
// It returns the action so the caller can replace the content when it is "blank".
func (s *Session) file(kind, name, ext string, content []byte) string {
	act := s.fileAction(kind, ext)
	st := statAction[act]
	s.count(kind, st)
	s.event(kind, st, name, "")
	if act != "pass" && act != "blank" {
		e := FileEvent{Kind: kind, Name: name, Ext: ext, Action: act, Size: len(content)}
		if len(content) > 0 {
			h := sha256.Sum256(content)
			e.SHA = hex.EncodeToString(h[:])
		}
		s.FileLog = append(s.FileLog, e)
	}
	if st == "block" {
		s.BlockedFiles = append(s.BlockedFiles, name)
		if act == "block_alert" {
			s.AlertFiles = append(s.AlertFiles, name)
		}
	}
	return act
}

// maskFileNames pseudonymizes the file names kept for the file log, the alerts and the block
// message ("Bilan_Dupont_90010112395.pdf" -> "Bilan_«PERSON_1»_«NISS_1».pdf"), once the values
// of the request are known.
func (s *Session) maskFileNames() {
	for i := range s.FileLog {
		s.FileLog[i].Name = s.fileName(s.FileLog[i].Name)
	}
	for i := range s.BlockedFiles {
		s.BlockedFiles[i] = s.fileName(s.BlockedFiles[i])
	}
	for i := range s.AlertFiles {
		s.AlertFiles[i] = s.fileName(s.AlertFiles[i])
	}
}

// fileName: a file name with every detected value replaced by its token; underscores, dashes
// and dots also separate words, the extension is kept.
func (s *Session) fileName(name string) string {
	stem, ext := name, ""
	if i := strings.LastIndex(name, "."); i > 0 && len(name)-i <= 8 {
		stem, ext = name[:i], name[i:]
	}
	if strings.TrimSpace(stem) == "" {
		return name
	}
	sep := strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || r == '.' {
			return ' '
		}
		return r
	}, stem) // same byte length: both texts share their offsets
	d1, d2 := s.detect(stem), s.detect(sep)
	all := s.addLearned(d1.tv.s, append([]span(nil), d1.sps...))
	all = s.addLearned(d2.tv.s, append(all, d2.sps...))
	sps := s.resolve(d2, all)
	if len(sps) == 0 {
		return name
	}
	var b strings.Builder
	last := 0
	for _, sp := range sps {
		b.WriteString(stem[last:sp.s])
		b.WriteString(s.token(sp, stem[sp.s:sp.e]))
		last = sp.e
	}
	b.WriteString(stem[last:])
	return b.String() + ext
}

// documents attached in the chat portal travel as extracted text between these markers
var (
	reDocMarker = regexp.MustCompile(`--- Document joint : (.+?) ---`)
	reDocBlock  = regexp.MustCompile(`(?s)(--- Document joint : (.+?) ---\n)(.*?)(\n--- Fin du document ---)`)
)

const blankDoc = "[contenu remplacé par le gateway]"

func extOf(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 && i < len(name)-1 {
		return normExt(name[i+1:])
	}
	return ""
}

func (s *Session) docMarkers(t string) string {
	if !strings.Contains(t, "--- Document joint") {
		return t
	}
	t = reDocBlock.ReplaceAllStringFunc(t, func(m string) string {
		g := reDocBlock.FindStringSubmatch(m)
		name := strings.TrimSpace(g[2])
		if s.file("FILE", name, extOf(name), []byte(g[3])) == "blank" {
			return g[1] + blankDoc + g[4]
		}
		return m
	})
	// markers without an end marker (other clients): the extension is all we can check
	for _, ix := range reDocMarker.FindAllStringSubmatchIndex(t, -1) {
		if strings.Contains(t[ix[1]:], "--- Fin du document ---") {
			continue
		}
		name := strings.TrimSpace(t[ix[2]:ix[3]])
		s.file("FILE", name, extOf(name), nil)
	}
	return t
}

// fileBytes decodes the inline content of a file or image part (data: URL or base64), if any.
func fileBytes(m map[string]any, typ string) []byte {
	var b []byte
	look := func(x map[string]any) {
		for _, k := range []string{"url", "file_data", "data", "image_url"} {
			v, _ := x[k].(string)
			if v == "" || b != nil {
				continue
			}
			if strings.HasPrefix(v, "data:") {
				if i := strings.Index(v, ","); i > 0 {
					v = v[i+1:]
				}
			} else if k == "url" || k == "image_url" {
				continue
			}
			if d, err := base64.StdEncoding.DecodeString(v); err == nil {
				b = d
			}
		}
	}
	look(m)
	for _, k := range []string{typ, "source", "file", "image_url"} {
		if sub, ok := m[k].(map[string]any); ok {
			look(sub)
		}
	}
	return b
}

// whitePNG: a 256×256 white square, sent to the LLM in place of a file or image set to "blank"
var whitePNG = func() string {
	img := image.NewGray(image.Rect(0, 0, 256, 256))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}()

// blankPart: the white square in the content-part format of the client's API.
func (s *Session) blankPart(typ string) map[string]any {
	uri := "data:image/png;base64," + whitePNG
	switch {
	case s.api == "anthropic":
		return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": whitePNG}}
	case strings.HasPrefix(typ, "input_"):
		return map[string]any{"type": "input_image", "image_url": uri}
	}
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": uri}}
}

// stripGwName removes the file name the chat portal adds for the file log ("gw_name"):
// it is not part of the LLM APIs.
func stripGwName(m map[string]any) {
	delete(m, "gw_name")
	for _, v := range m {
		if sub, ok := v.(map[string]any); ok {
			delete(sub, "gw_name")
		}
	}
}

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
					name, ext := fileOf(m, t)
					stripGwName(m)
					if name == "" {
						name = "[" + t + "]"
					}
					if s.file(mediaKinds[t], name, ext, fileBytes(m, t)) == "blank" {
						x[i] = s.blankPart(t)
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

// walk modes
const (
	modeStruct = iota // API structure: only the text fields are walked
	modeText          // user text: structural keys (type, id, signatures…) are skipped
	modeRaw           // tool inputs, metadata: every string is data
	modeSchema        // tool definitions: descriptions, titles, enums, defaults and examples only
)

var (
	textKeys = map[string]bool{"content": true, "text": true, "system": true, "prompt": true, "instructions": true}
	rawKeys  = map[string]bool{"input": true, "arguments": true, "metadata": true} // every string is data
	skipKeys = map[string]bool{"type": true, "role": true, "id": true, "model": true, "media_type": true, "url": true,
		"data": true, "tool_use_id": true, "tool_call_id": true, "name": true, "cache_control": true, "source": true,
		"image_url": true, "file_id": true, "file_data": true, "thinking": true, "signature": true}
	keepKeys = map[string]bool{"thinking": true, "signature": true, "data": true} // never rewritten (signed / binary)
	// JSON Schema of the tools: the values of these keys are text, these maps hold sub-schemas
	schemaText = map[string]bool{"description": true, "title": true, "enum": true, "examples": true, "default": true, "const": true}
	schemaMaps = map[string]bool{"properties": true, "patternProperties": true, "$defs": true, "definitions": true}
	schemaSkip = map[string]bool{"type": true, "name": true, "format": true, "pattern": true, "required": true, "$ref": true,
		"$schema": true, "$id": true, "cache_control": true, "strict": true}
	// reasoning text of an assistant message (OpenAI-compatible APIs)
	reasoningKeys = []string{"reasoning", "reasoning_content"}
	// tool arguments travel as JSON text: values restored there are JSON-escaped
	jsonTextKeys = map[string]bool{"arguments": true, "partial_json": true}
)

// encrypted blobs (encrypted_content, encrypted_index…) are opaque: never scanned nor rewritten
func opaqueKey(k string) bool { return strings.HasPrefix(k, "encrypted") }

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks) // deterministic numbering => stable prompts for upstream caching
	return ks
}

// walk applies fn to the user-content strings of a part of a request (see the modes).
func walk(v any, mode int, fn func(string) string) any {
	switch x := v.(type) {
	case map[string]any:
		if mode == modeSchema {
			walkSchema(x, fn)
			return v
		}
		if mode != modeRaw {
			walkDocument(x, fn)
		}
		for _, k := range sortedKeys(x) {
			if opaqueKey(k) || mode != modeRaw && skipKeys[k] {
				continue
			}
			m := mode
			if rawKeys[k] {
				m = modeRaw
			} else if m == modeStruct && textKeys[k] {
				m = modeText
			}
			x[k] = walk(x[k], m, fn)
		}
	case []any:
		for i := range x {
			x[i] = walk(x[i], mode, fn)
		}
	case string:
		if mode == modeText || mode == modeRaw {
			return fn(x)
		}
	}
	return v
}

// walkDocument: the text of an Anthropic document block (source of type "text" or "content");
// base64 and URL sources are left alone.
func walkDocument(x map[string]any, fn func(string) string) {
	if t, _ := x["type"].(string); t != "document" {
		return
	}
	src, _ := x["source"].(map[string]any)
	switch t, _ := src["type"].(string); t {
	case "text":
		if d, ok := src["data"].(string); ok {
			src["data"] = fn(d)
		}
	case "content":
		src["content"] = walk(src["content"], modeText, fn)
	}
}

func walkSchema(x map[string]any, fn func(string) string) {
	for _, k := range sortedKeys(x) {
		switch {
		case opaqueKey(k) || schemaSkip[k]:
		case schemaText[k]:
			x[k] = walk(x[k], modeRaw, fn)
		case schemaMaps[k]:
			if m, ok := x[k].(map[string]any); ok { // property names are not walked, their schemas are
				for _, p := range sortedKeys(m) {
					m[p] = walk(m[p], modeSchema, fn)
				}
			}
		default:
			x[k] = walk(x[k], modeSchema, fn)
		}
	}
}

// restoreAll applies fn to every string of a response (placeholders are unambiguous).
func restoreAll(v any, fn func(string) string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			if keepKeys[k] || opaqueKey(k) {
				continue
			}
			if t, ok := c.(string); ok && jsonTextKeys[k] {
				x[k] = jsonSafe(fn)(t)
				continue
			}
			x[k] = restoreAll(c, fn)
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
	raw *strings.Builder // debug: text as produced by the LLM, before restore
}

func newRestorer(s *Session) *Restorer { return &Restorer{s: s, buf: map[rkey]string{}} }

// restore: tool arguments (OpenAI "arguments", Anthropic "partial_json") are JSON text.
func (r *Restorer) restore(k rkey, t string) string {
	if jsonTextKeys[k.f] {
		return r.s.RestoreJSON(t)
	}
	return r.s.Restore(t)
}

func (r *Restorer) Push(k rkey, t string) string {
	if r.raw != nil && (k.f == "content" || k.f == "text") {
		r.raw.WriteString(t)
	}
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
	return r.restore(k, t[:cut])
}

func (r *Restorer) Flush(k rkey) string {
	t := r.buf[k]
	delete(r.buf, k)
	return r.restore(k, t)
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

// ---------- cross-request cache of detector spans ----------

// Clients such as Claude Code send the whole conversation again at every turn: the spans the
// detectors found in a text are kept, per configuration, so that only the new texts are
// scanned. Detectors that depend on mutable state (the persistent name memory) always run;
// the learned values of the request, the allow list and the profile's actions are applied
// per request on top of the cached spans.
const cacheMinLen = 256 // shorter texts are cheaper to scan than to look up

type cacheKey struct {
	h   [32]byte
	cfg *Config
}

type cacheEntry struct {
	key  cacheKey
	dets []*Detector
	sps  [][]span // per detector, view offsets (never modified once stored)
	size int
}

func (e *cacheEntry) lookup(d *Detector) ([]span, bool) {
	for i, x := range e.dets {
		if x == d {
			return e.sps[i], true
		}
	}
	return nil, false
}

type spanCache struct {
	mu           sync.Mutex
	ll           *list.List
	m            map[cacheKey]*list.Element
	bytes        int
	maxN, maxB   int
	hits, misses int
}

// detCache: at most 20 000 texts or about 64 MB of spans.
var detCache = newSpanCache(20000, 64<<20)

func newSpanCache(n, b int) *spanCache {
	return &spanCache{ll: list.New(), m: map[cacheKey]*list.Element{}, maxN: n, maxB: b}
}

func (c *spanCache) get(k cacheKey) *cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[k]; ok {
		c.ll.MoveToFront(el)
		c.hits++
		return el.Value.(*cacheEntry)
	}
	c.misses++
	return nil
}

// add stores the spans just computed (todo) next to those of old, if any.
func (c *spanCache) add(k cacheKey, old *cacheEntry, dets []*Detector, todo []int, res [][]span) {
	e := &cacheEntry{key: k, size: 128}
	if old != nil {
		e.dets, e.sps = append(e.dets, old.dets...), append(e.sps, old.sps...)
	}
	for _, i := range todo {
		if !dets[i].volatile() {
			e.dets, e.sps = append(e.dets, dets[i]), append(e.sps, res[i])
		}
	}
	for _, sp := range e.sps {
		e.size += 32 + len(sp)*96
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[k]; ok {
		c.bytes -= el.Value.(*cacheEntry).size
		el.Value = e
		c.ll.MoveToFront(el)
	} else {
		c.m[k] = c.ll.PushFront(e)
	}
	c.bytes += e.size
	for c.ll.Len() > c.maxN || c.bytes > c.maxB {
		el := c.ll.Back()
		old := el.Value.(*cacheEntry)
		c.ll.Remove(el)
		delete(c.m, old.key)
		c.bytes -= old.size
	}
}
