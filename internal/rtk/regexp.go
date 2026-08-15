package rtk

import "regexp"

// mustCompile panics on invalid regexps — a programmer error, caught at
// startup by the test suite.
func mustCompile(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}

// count returns the number of matches of re in s.
func count(re *regexp.Regexp, s string) int {
	n := 0
	for range re.FindAllStringIndex(s, -1) {
		n++
	}
	return n
}
