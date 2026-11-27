package detect

import "regexp"

var ssnRE = regexp.MustCompile(`\b(\d{3})-(\d{2})-(\d{4})\b`)

// SSN returns a detector for US Social Security numbers written as
// 123-45-6789. It skips numbers the SSA never issues: area 000, 666 or
// 900-999, group 00, and serial 0000.
func SSN() *Pattern {
	return &Pattern{kind: "ssn", re: ssnRE, valid: validSSN}
}

func validSSN(g []string) bool {
	area, group, serial := g[0], g[1], g[2]
	if area == "000" || area == "666" || area[0] == '9' {
		return false
	}
	return group != "00" && serial != "0000"
}
