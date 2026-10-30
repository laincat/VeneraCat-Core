package js

import "strings"

// splitLines normalises CRLF and splits source into lines.
func splitLines(source string) []string {
	return strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
}
