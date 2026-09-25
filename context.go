package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Context detectors: identifiers introduced by a keyword ("Dossier patient 676888-67")
// and dates (all of them, or birth dates only: "né le 30/12/2006").

var refKeywords = map[string]bool{"dossier": true, "patient": true, "patiente": true, "client": true, "cliente": true,
	"contrat": true, "facture": true, "commande": true, "matricule": true, "reference": true, "ref": true, "numero": true,
	"n°": true, "no": true, "num": true, "nr": true, "nummer": true, "dossiernummer": true, "file": true, "case": true,
	"id": true, "identifiant": true, "ticket": true, "sinistre": true, "police": true, "badge": true, "employe": true,
	"adherent": true, "affilie": true, "membre": true, "mutuelle": true, "patientnummer": true, "klantnummer": true, "account": true}

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
		if !refKeywords[w] {
			continue
		}
		for j := i + 1; j < len(locs) && j <= i+3; j++ {
			t := strings.TrimRight(s[locs[j][0]:locs[j][1]], ".")
			if reIdent.MatchString(t) && digits(t) >= 3 && !isDate(t) {
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

var birthCues = []string{"ne le", "nee le", "ne(e) le", "ne ", "nee ", "naissance", "dob", "born", "geboren", "geb.", "date of birth", "birth"}

func findBirthdates(s string) [][2]int {
	var out [][2]int
	for _, m := range findDates(s) {
		before := fold(s[max(0, m[0]-30):m[0]])
		for _, c := range birthCues {
			if strings.Contains(before, c) {
				out = append(out, m)
				break
			}
		}
	}
	return out
}
