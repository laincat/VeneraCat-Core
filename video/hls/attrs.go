package hls

import "strings"

// attr is one NAME=VALUE pair of an attribute list. start and end delimit the
// value token (including quotes) inside the string given to parseAttrs.
type attr struct {
	Name       string
	Value      string
	Quoted     bool
	start, end int
}

// parseAttrs splits an attribute list. Quoted values may contain commas; a
// missing closing quote runs to the end of the list. Tokens without '=' are
// skipped.
func parseAttrs(s string) []attr {
	var out []attr
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ',' || s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		j := i
		for j < len(s) && s[j] != '=' && s[j] != ',' {
			j++
		}
		if j >= len(s) || s[j] == ',' {
			i = j
			continue
		}
		a := attr{Name: strings.TrimSpace(s[i:j])}
		j++
		a.start = j
		if j < len(s) && s[j] == '"' {
			a.Quoted = true
			if k := strings.IndexByte(s[j+1:], '"'); k < 0 {
				a.Value = s[j+1:]
				a.end = len(s)
			} else {
				a.Value = s[j+1 : j+1+k]
				a.end = j + 1 + k + 1
			}
		} else {
			a.end = len(s)
			if k := strings.IndexByte(s[j:], ','); k >= 0 {
				a.end = j + k
			}
			a.Value = strings.TrimSpace(s[j:a.end])
		}
		out = append(out, a)
		i = a.end
	}
	return out
}

func attrValue(a []attr, name string) string {
	for _, x := range a {
		if strings.EqualFold(x.Name, name) {
			return x.Value
		}
	}
	return ""
}
