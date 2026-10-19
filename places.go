package main

import (
	_ "embed"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Places, addresses, pets, organisations and first names on their own.

//go:embed places_be.txt
var placesTxt string

//go:embed health_orgs_be.txt
var healthOrgsTxt string

func lines(txt string) []string {
	var out []string
	for _, l := range strings.Split(txt, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

var placeList = lines(placesTxt)
var healthOrgs = lines(healthOrgsTxt)

var placeSet = func() map[string]bool {
	m := map[string]bool{}
	for _, p := range placeList {
		m[fold(p)] = true
	}
	return m
}()

var (
	streetTypes = `rue|avenue|av\.|chaussée|chée|boulevard|bd|place|square|quai|allée|impasse|chemin|route|drève|clos|sentier|venelle|straat|laan|steenweg|lei|plein|kaai|dreef|weg|street|road|lane`
	// "rue de la Loi 16", "Avenue Louise 54 bte 3", "Chaussée de Waterloo, 1180"
	reStreet = regexp.MustCompile(`(?i)\b(?:` + streetTypes + `)\s+(?:(?:de la|de l'|du|des|de|d'|van|van de|van der|der|den|la|le|les)\s+)?[\p{L}'’\-]+(?:\s+[\p{Lu}][\p{L}'’\-]+){0,3},?\s+\d{1,4}[a-zA-Z]?(?:\s*(?:bte|boîte|bus|b)\.?\s*\d+)?\b`)
	// Dutch compounds: "Kerkstraat 12", "Stationsplein 3"
	reStreetNL = regexp.MustCompile(`\b[\p{Lu}][\p{L}\-]+(?:straat|laan|steenweg|weg|lei|plein|dreef|kaai|singel|markt|pad)\s+\d{1,4}[a-zA-Z]?(?:\s*(?:bus|b)\s*\d+)?\b`)
	// "1000 Bruxelles", "B-6041 Gosselies"
	rePostal = regexp.MustCompile(`\b(B-|BE-)?([1-9]\d{3})\s+([\p{Lu}][\p{L}'’\-]+(?:[ \-][\p{Lu}][\p{L}'’\-]+){0,3})`)
	// reStreet is only tried where a street type starts
	streetList = func() *wordList {
		w := &wordList{anch: regexp.MustCompile(`^` + reStreet.String()), starts: map[string]bool{}, fold: true, asciiB: true}
		for _, t := range strings.Split(streetTypes, "|") {
			w.starts[w.key(strings.TrimSuffix(strings.ReplaceAll(t, `\.`, "."), "."))] = true
		}
		return w
	}()
)

func findAddresses(s string) [][2]int {
	var out [][2]int
	for _, m := range append(streetList.find(s), reStreetNL.FindAllStringIndex(s, -1)...) {
		out = append(out, [2]int{m[0], m[1]})
	}
	for _, m := range rePostal.FindAllStringSubmatchIndex(s, -1) {
		loc := s[m[6]:m[7]]
		first := strings.Fields(strings.ReplaceAll(loc, "-", " "))[0]
		before := strings.TrimRight(s[max(0, m[0]-3):m[0]], " ")
		// a year followed by a word is not an address: require the B- prefix, a known locality,
		// or an address layout (after a comma or a line break)
		if m[2] >= 0 || placeSet[fold(loc)] || placeSet[fold(first)] || strings.HasSuffix(before, ",") || strings.HasSuffix(before, "\n") || m[0] == 0 {
			if !commonWord(first) || placeSet[fold(first)] {
				out = append(out, [2]int{m[0], m[1]})
			}
		}
	}
	return out
}

var placeCues = []string{"domicilie a", "domiciliee a", "habite a", "habite au", "reside a", "residant a", "residente a", "ne a", "nee a",
	"originaire de", "demeurant a", "woont in", "wonende te", "geboren in", "geboren te", "lives in", "born in", "resident of"}

// findPlaces: localities of the list, and capitalized words after a residence cue.
func findPlaces(s string) [][2]int {
	var out [][2]int
	ts := tokenize(s)
	for i := 0; i < len(ts); i++ {
		// multi-word localities ("Montignies-sur-Sambre" is one token; "La Louvière" two)
		for n := 3; n >= 1; n-- {
			if i+n > len(ts) {
				continue
			}
			v := s[ts[i].s:ts[i+n-1].e]
			if isCap(ts[i].w) && placeSet[fold(v)] && (n == 1 || gapOK(ts[i].w, s[ts[i].e:ts[i+1].s])) {
				out = append(out, [2]int{ts[i].s, ts[i+n-1].e})
				i += n - 1
				break
			}
		}
	}
	low := fold(s)
	for _, c := range placeCues {
		for off := 0; ; {
			k := strings.Index(low[off:], c+" ")
			if k < 0 {
				break
			}
			start := off + k + len(c) + 1
			off = start
			// map the folded offset back: fold keeps byte lengths for most accents only approximately,
			// so re-tokenize the original text from the same rune position
			pos := byteOffsetForFolded(s, start)
			rest := tokenize(s[pos:])
			if len(rest) > 0 && isCap(rest[0].w) && !commonWord(rest[0].w) {
				last, _ := nameAfter(s[pos:], rest, 0)
				if last >= 0 {
					out = append(out, [2]int{pos + rest[0].s, pos + rest[last].e})
				}
			}
		}
	}
	return out
}

// byteOffsetForFolded converts a byte offset in fold(s) into a byte offset in s (same rune count).
func byteOffsetForFolded(s string, foldedOff int) int {
	f := fold(s)
	runes := utf8.RuneCountInString(f[:foldedOff])
	i := 0
	for n := 0; n < runes && i < len(s); n++ {
		_, sz := utf8.DecodeRuneInString(s[i:])
		i += sz
	}
	return i
}

var petCues = map[string]bool{"chien": true, "chienne": true, "chiot": true, "chat": true, "chatte": true, "chaton": true, "cheval": true,
	"jument": true, "poney": true, "lapin": true, "perroquet": true, "hamster": true, "furet": true, "hond": true, "kat": true, "poes": true,
	"paard": true, "konijn": true, "dog": true, "cat": true, "horse": true, "puppy": true, "kitten": true}

// findPets: "mon chien Rex", "le chat nommé Félix", "onze hond Max".
// petDeterminers: a pet cue counts only after a determiner ("mon chien Rex", "our dog Rex"):
// "chat" alone is mostly the English word (chat completions, the chat window).
var petDeterminers = map[string]bool{"mon": true, "ma": true, "ton": true, "ta": true, "son": true, "sa": true, "notre": true,
	"votre": true, "leur": true, "le": true, "la": true, "un": true, "une": true, "my": true, "our": true, "your": true,
	"his": true, "her": true, "their": true, "the": true, "a": true, "mijn": true, "onze": true, "jouw": true, "zijn": true,
	"haar": true, "hun": true, "de": true, "het": true, "een": true}

// petNaming: words allowed between the cue and the name ("le chat nommé Félix", "a dog called Rex").
var petNaming = map[string]bool{"nomme": true, "nommee": true, "appele": true, "appelee": true, "qui": true, "s": true,
	"appelle": true, "named": true, "called": true, "genaamd": true, "heet": true, "die": true}

func findPets(s string) [][2]int {
	ts := tokenize(s)
	var out [][2]int
	for i := 1; i < len(ts)-1; i++ {
		if !petCues[fold(ts[i].w)] || !petDeterminers[fold(ts[i-1].w)] || !gapOK(ts[i-1].w, s[ts[i-1].e:ts[i].s]) {
			continue
		}
		j := i + 1
		for j < len(ts) && j <= i+3 && petNaming[fold(ts[j].w)] && gapOK(ts[j-1].w, s[ts[j-1].e:ts[j].s]) {
			j++
		}
		if j >= len(ts) || j > i+3 || !gapOK(ts[j-1].w, s[ts[j-1].e:ts[j].s]) {
			continue
		}
		w := ts[j].w
		// a pet's name is a proper noun: capitalized, not an ordinary word ("The", "Image", "What")
		if isCap(w) && !isAllCaps(w) && utf8.RuneCountInString(w) >= 2 && !commonWord(w) && !defaultAllow[fold(w)] {
			out = append(out, [2]int{ts[j].s, ts[j].e})
			i = j
		}
	}
	return out
}

// first names that are also ordinary French words
var ambiguousFirst = map[string]bool{"rose": true, "olive": true, "victoire": true, "constance": true, "prudence": true, "france": true,
	"blanche": true, "marine": true, "aurore": true, "ange": true, "violette": true, "capucine": true, "jade": true, "perle": true,
	"iris": true, "fleur": true, "claire": true, "pierre": true, "sylvain": true, "prosper": true, "modeste": true, "sage": true, "pascale": true, "noel": true, "desire": true, "aime": true, "rene": true,
	"can": true, "stan": true, "bob": true, "guy": true, "art": true, "will": true, "mark": true, "grace": true, "hope": true,
	// English first names that are also everyday words or months
	"may": true, "june": true, "april": true, "august": true, "ray": true, "sue": true, "pat": true, "rob": true, "don": true,
	"joy": true, "ivy": true, "eve": true, "kit": true, "jay": true, "mac": true, "rex": true, "sal": true, "ace": true,
	"del": true, "les": true, "mo": true, "ty": true, "al": true, "ed": true, "bo": true, "jo": true, "sy": true, "val": true,
	"dee": true, "flo": true, "ike": true, "nan": true, "norm": true, "rod": true, "russ": true, "tad": true, "len": true,
	"lee": true, "ken": true, "gene": true, "dean": true, "chase": true, "frank": true, "rich": true, "dawn": true, "faith": true,
	// first names that are also technical words
	"max": true, "neo": true, "ruby": true, "rust": true, "kai": true, "ada": true, "nova": true, "echo": true}

// findFirstnameAlone: a known first name on its own ("Luc a rappelé"), unless it is also an ordinary word.
func findFirstnameAlone(s string) [][2]int {
	var out [][2]int
	for _, t := range tokenize(s) {
		f := fold(t.w)
		// dictionary words of 4+ letters (Will, Mark, Hope…) are left alone; short names (Tom, Max, Jan) are kept
		if isCap(t.w) && !isAllCaps(t.w) && firstnames[f] && !ambiguousFirst[f] && !defaultAllow[f] && !(commonWord(t.w) && utf8.RuneCountInString(f) >= 4) {
			out = append(out, [2]int{t.s, t.e})
		}
	}
	return out
}
