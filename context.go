package main

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Context detectors: identifiers introduced by a keyword ("Dossier patient 676888-67")
// and dates (all of them, or birth dates only: "né le 30/12/2006").

// French/Dutch business keywords only: English "id", "no", "file", "case"… are everywhere in technical text.
var refKeywords = map[string]bool{"dossier": true, "patient": true, "patiente": true, "client": true, "cliente": true,
	"contrat": true, "facture": true, "commande": true, "matricule": true, "reference": true, "ref": true, "numero": true,
	"n°": true, "nr": true, "nummer": true, "dossiernummer": true, "identifiant": true, "sinistre": true, "police": true,
	"badge": true, "employe": true, "adherent": true, "affilie": true, "membre": true, "mutuelle": true,
	"patientnummer": true, "klantnummer": true}

// looksLikeRef: 5+ digits, or 3+ digits in a structured form (AB-123, 2025/77).
func looksLikeRef(t string) bool {
	if !reIdent.MatchString(t) || isDate(t) {
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
)

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

func findBirthdates(s string) [][2]int {
	var out [][2]int
	for _, m := range findDates(s) {
		if birthCue(strings.ToValidUTF8(s[max(0, m[0]-30):m[0]], "")) {
			out = append(out, m)
		}
	}
	return out
}
