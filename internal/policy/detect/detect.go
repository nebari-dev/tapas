// Package detect finds sensitive values in text. Detectors add findings; they
// never decide what to do about them. The caller (the memory write check now,
// a policy engine later) owns that decision.
package detect

import "regexp"

// Finding is one match in a text. Start and End are byte offsets into the
// text, so text[Start:End] is the matched value. Confidence runs from 0 to 1.
type Finding struct {
	Kind       string
	Start, End int
	Confidence float64
}

// Pattern is a detector built from a regular expression plus an optional
// check that drops matches the expression alone cannot rule out.
type Pattern struct {
	kind  string
	re    *regexp.Regexp
	valid func(groups []string) bool
}

// Find returns every match in text that passes the pattern's check, in the
// order they appear.
func (p *Pattern) Find(text string) []Finding {
	var out []Finding
	for _, m := range p.re.FindAllStringSubmatchIndex(text, -1) {
		groups := make([]string, 0, len(m)/2-1)
		for i := 2; i+1 < len(m); i += 2 {
			groups = append(groups, text[m[i]:m[i+1]])
		}
		if p.valid != nil && !p.valid(groups) {
			continue
		}
		out = append(out, Finding{Kind: p.kind, Start: m[0], End: m[1], Confidence: 1})
	}
	return out
}
