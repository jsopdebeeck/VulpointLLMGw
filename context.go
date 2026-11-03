package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Context detectors: identifiers introduced by a keyword ("Dossier patient 676888-67"),
// dates (all of them, or birth dates only: "né le 30/12/2006") and IP addresses.

// French/Dutch business keywords only: English "id", "no", "file", "case"… are everywhere in technical text.
var refKeywords = map[string]bool{"dossier": true, "patient": true, "patiente": true, "client": true, "cliente": true,
	"contrat": true, "facture": true, "commande": true, "matricule": true, "reference": true, "ref": true, "numero": true,
	"n°": true, "nr": true, "nummer": true, "dossiernummer": true, "identifiant": true, "sinistre": true, "police": true,
	"badge": true, "employe": true, "adherent": true, "affilie": true, "membre": true, "mutuelle": true,
	"patientnummer": true, "klantnummer": true}

// looksLikeRef: 5+ digits, or 3+ digits in a structured form (AB-123, 2025/77).
func looksLikeRef(t string) bool {
	if !reIdent.MatchString(t) || isDate(t) || reNumList.MatchString(t) || notARef(t) {
		return false
	}
	d := digits(t)
	return d >= 5 || (d >= 3 && (strings.ContainsAny(t, "-/._") || d < len(t)))
}

// inCode: the keyword is part of code or JSON ("id": 622, patient_id=622, obj.patient).
func inCode(s string, a, b int) bool {
	if a > 0 && strings.ContainsRune("\"'_.$`", rune(s[a-1])) {
		return true
	}
	rest := strings.TrimLeft(s[b:], " ")
	return strings.HasPrefix(rest, "\"") || strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, "_")
}

var (
	reWordTok = regexp.MustCompile(`[^\s,;:()\[\]{}"«»]+`)
	reIdent   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9\-/._]*[A-Za-z0-9]$`)
	reNumList = regexp.MustCompile(`^\d{1,3}(?:/\d{1,3}){2,}$`) // scores or a list ("96/63/90/94"), not "2025/77"
	reVersion = regexp.MustCompile(`^[vV]\d+(?:\.\d+)+$`)       // "v1.22"
	reDayMon  = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})$`)     // "11/10", not "2025/77"
	reSpan    = regexp.MustCompile(`^(\d{1,2})-(\d{1,2})$`)     // lines "46-50", not "676888-67"
	reHexID   = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
)

// notARef: a version, a day and month, a range, or a commit or checksum.
func notARef(t string) bool {
	if reVersion.MatchString(t) || hexDigest(t) {
		return true
	}
	if m := reDayMon.FindStringSubmatch(t); m != nil {
		d, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		return d >= 1 && d <= 31 && mo >= 1 && mo <= 12
	}
	if m := reSpan.FindStringSubmatch(t); m != nil {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		return a < b
	}
	return false
}

// hexDigest: lowercase hexadecimal where letters and digits alternate ("ad6f0b4"); a code such
// as "abc1234" stays a reference.
func hexDigest(t string) bool {
	if !reHexID.MatchString(t) {
		return false
	}
	n := 0
	for i := 1; i < len(t); i++ {
		if isDigitByte(t[i]) != isDigitByte(t[i-1]) {
			n++
		}
	}
	return n >= 3
}

func digits(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}

// findRefContext: the first identifier-like token (3+ digits) within 3 words after a keyword.
func findRefContext(s string) [][2]int {
	locs := reWordTok.FindAllStringIndex(s, -1)
	var out [][2]int
	for i := 0; i < len(locs); i++ {
		w := strings.TrimRight(fold(s[locs[i][0]:locs[i][1]]), ".:#")
		if !refKeywords[w] || inCode(s, locs[i][0], locs[i][1]) {
			continue
		}
		for j := i + 1; j < len(locs) && j <= i+3; j++ {
			t := strings.TrimRight(s[locs[j][0]:locs[j][1]], ".")
			if looksLikeRef(t) {
				out = append(out, [2]int{locs[j][0], locs[j][0] + len(t)})
				i = j
				break
			}
			if digits(t) > 0 || strings.ContainsAny(s[locs[j-1][1]:locs[j][0]], ".!?\n") {
				break // another number or a new sentence: no identifier here
			}
		}
	}
	return out
}

var months = `janvier|février|fevrier|mars|avril|mai|juin|juillet|août|aout|septembre|octobre|novembre|décembre|decembre|` +
	`january|february|march|april|may|june|july|august|september|october|november|december|` +
	`januari|februari|maart|mei|juni|juli|augustus|oktober`

var reDate = regexp.MustCompile(`(?i)\b(\d{1,2})[/.\-](\d{1,2})[/.\-](\d{4}|\d{2})\b|\b(\d{4})-(\d{2})-(\d{2})\b|\b\d{1,2}(?:er)?\s+(?:` + months + `)\s+\d{4}\b`)

func isDate(t string) bool {
	m := reDate.FindString(t)
	return m != "" && len(m) == len(t) && validDate(t)
}

func validDate(t string) bool {
	m := reDate.FindStringSubmatch(t)
	if m == nil {
		return false
	}
	d, mo := m[1], m[2]
	if m[4] != "" {
		d, mo = m[6], m[5]
	}
	if d == "" { // written month
		return true
	}
	dd, _ := strconv.Atoi(d)
	mm, _ := strconv.Atoi(mo)
	return dd >= 1 && dd <= 31 && mm >= 1 && mm <= 12
}

func findDates(s string) [][2]int {
	var out [][2]int
	for _, m := range reDate.FindAllStringIndex(s, -1) {
		if validDate(s[m[0]:m[1]]) {
			out = append(out, [2]int{m[0], m[1]})
		}
	}
	return out
}

// birthCues: whole words before a date of birth (not inside "une", "deadline", "online").
var birthCues = map[string]bool{"naissance": true, "dob": true, "born": true, "birth": true, "birthday": true, "birthdate": true,
	"geboren": true, "geb": true, "geboortedatum": true}

func notAlnum(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }

// birthCue: a cue word in the text before a date. "né", "née" need their accent or "le" after
// them: a bare "ne" is the French negation.
func birthCue(before string) bool {
	before = withoutGlued(before)
	ws := strings.FieldsFunc(fold(before), notAlnum)
	for i, w := range ws {
		if birthCues[w] || (w == "ne" || w == "nee") && i+1 < len(ws) && ws[i+1] == "le" {
			return true
		}
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(before), notAlnum) {
		switch w {
		case "né", "née", "nés", "nées":
			return true
		}
	}
	return false
}

// dateParts: what a cue may be joined to ("date-de-naissance", "date-of-birth").
var dateParts = map[string]bool{"date": true, "de": true, "of": true}

// withoutGlued drops the tokens where a cue is only part of a name: a path
// ("archive/profil-naissance/export.tar") or a compound ("profil-alpha-naissance").
func withoutGlued(before string) string {
	toks := strings.FieldsFunc(before, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune("\"',;|()[]{}=<>:", r) })
	kept := toks[:0]
	for _, t := range toks {
		if !pathLike(t) && !gluedCue(t) {
			kept = append(kept, t)
		}
	}
	return strings.Join(kept, " ")
}

// pathLike: "naissance/décès" is a label, "a/b/c", "x/y.tar" and "dir/" are paths.
func pathLike(t string) bool {
	return strings.Contains(t, `\`) || strings.ContainsAny(t[:1], "/~.") || strings.HasSuffix(t, "/") ||
		strings.Count(t, "/") >= 2 || strings.Contains(t, "/") && strings.Contains(t, ".")
}

func gluedCue(t string) bool {
	parts := strings.Split(fold(t), "-")
	cue, other := false, false
	for _, p := range parts {
		p = strings.TrimFunc(p, notAlnum)
		cue = cue || birthCues[p]
		other = other || !birthCues[p] && !dateParts[p]
	}
	return cue && other
}

func findBirthdates(s string) [][2]int {
	var out [][2]int
	for _, m := range findDates(s) {
		if !recentDate(s[m[0]:m[1]]) && birthCue(s[tokenStart(s, wordStart(s, max(0, m[0]-30), m[0]-94), m[0]-94):m[0]]) {
			out = append(out, m)
		}
	}
	return out
}

// tokenStart moves a text window back to the space before the token it cuts, so that a compound
// is read whole. It stops at limit.
func tokenStart(s string, a, limit int) int {
	for a > 0 && a > limit {
		r, _ := utf8.DecodeLastRuneInString(s[:a])
		if utf8.RuneStart(s[a]) && unicode.IsSpace(r) {
			break
		}
		a--
	}
	return a
}

// wordStart moves a text window back to the start of the word it cuts: the end of "consignée"
// reads as the cue "née". It stops at limit.
func wordStart(s string, a, limit int) int {
	for a > 0 && a > limit {
		r, _ := utf8.DecodeLastRuneInString(s[:a])
		if utf8.RuneStart(s[a]) && notAlnum(r) {
			break
		}
		a--
	}
	return a
}

// recentDate: a date of this year or the last, or to come, reads as an ordinary date (logs, file
// names). Not suited to neonatal records: their dates of birth are recent.
func recentDate(t string) bool {
	m := reDate.FindStringSubmatch(t)
	if m == nil {
		return false
	}
	y := m[3]
	if m[4] != "" {
		y = m[4]
	}
	if y == "" { // written month: the year ends the date
		y = t[len(t)-4:]
	}
	yr, _ := strconv.Atoi(y)
	now := time.Now().Year()
	if len(y) == 2 { // "03/11/26": 2026 rather than 1926
		yr += now - now%100
		return yr == now || yr == now-1
	}
	return yr >= now-1
}

var reIPv4 = regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b`)

var (
	reLocalIP = regexp.MustCompile(`^(?:10\.|127\.|192\.168\.|169\.254\.|172\.(?:1[6-9]|2\d|3[01])\.)`)
	// software shown with a four-part version; a provider's name ("Hetzner 88.1.2.3") is not listed
	versionedSoftware = map[string]bool{"chrome": true, "chromium": true, "edge": true, "firefox": true, "safari": true, "office": true,
		"windows": true, "android": true, "java": true, "python": true, "node": true, "openssl": true, "ios": true, "macos": true}
)

// findIPv4: dotted numbers, except where they read as a version ("LibreOffice 26.8.1.1",
// "firmware 1.0.5.2"). A private address is always an address.
func findIPv4(s string) [][2]int {
	var out [][2]int
	for _, m := range reIPv4.FindAllStringIndex(s, -1) {
		if reLocalIP.MatchString(s[m[0]:m[1]]) || !versionNumber(s, m[0], m[1]) {
			out = append(out, [2]int{m[0], m[1]})
		}
	}
	return out
}

func versionNumber(s string, a, b int) bool {
	before := strings.ToValidUTF8(s[wordStart(s, max(0, a-30), a-94):a], "")
	if i := strings.LastIndexByte(before, '\n'); i >= 0 {
		before = before[i+1:]
	}
	after := strings.ToValidUTF8(s[b:min(len(s), b+25)], "")
	lb, la := strings.ToLower(before), strings.ToLower(after)
	for _, c := range []string{"version", "build", "release", "firmware", "patch"} {
		if strings.Contains(lb, c) {
			return true
		}
	}
	if strings.Contains(la, "firmware") {
		return true
	}
	ws := strings.FieldsFunc(before, notAlnum)
	if len(ws) == 0 || !strings.HasSuffix(before, ws[len(ws)-1]+" ") {
		return false
	}
	w := ws[len(ws)-1]
	return versionedSoftware[strings.ToLower(w)] || camelCase(w)
}

// camelCase: a capital inside a word ("LibreOffice", "PowerShell"), not an all-caps word.
func camelCase(w string) bool {
	rs := []rune(w)
	lower := false
	for i, r := range rs {
		if unicode.IsLower(r) {
			lower = true
		} else if i > 0 && unicode.IsUpper(r) && lower {
			return true
		}
	}
	return false
}
