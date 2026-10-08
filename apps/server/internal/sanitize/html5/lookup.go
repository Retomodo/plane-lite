package html

// LookupEntity returns the characters of the HTML5 named character
// reference name ("amp;", or a legacy name without the semicolon such as
// "amp"), the table Python's html.entities.html5 holds.
func LookupEntity(name string) (string, bool) {
	if r, ok := entity[name]; ok {
		return string(r), true
	}
	if rs, ok := entity2[name]; ok {
		return string(rs[:]), true
	}
	return "", false
}
