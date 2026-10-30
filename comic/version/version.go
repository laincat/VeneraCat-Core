// Package version compares comic-source versions the way Venera does.
package version

import (
	"strconv"
	"strings"
)

// Newer reports whether a is a later version than b.
//
// The rules follow the official parser: the first "-" becomes a "." so
// "1.2.3-beta" has a fourth part "beta"; the first three parts compare as
// integers; then a version without a fourth part is newer than one with any
// suffix except "hotfix", and "hotfix" is newer than no suffix; two suffixes
// compare as strings. Unlike the official parser, missing or non-numeric
// parts count as 0 instead of throwing.
func Newer(a, b string) bool {
	pa, sa := split(a)
	pb, sb := split(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	if sa == sb {
		return false
	}
	switch {
	case sa == "" && sb != "hotfix":
		return true
	case sa == "":
		return false
	case sb == "":
		return sa == "hotfix"
	}
	return sa > sb
}

func split(v string) ([3]int64, string) {
	v = strings.Replace(strings.TrimSpace(v), "-", ".", 1)
	parts := strings.Split(v, ".")
	var nums [3]int64
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.ParseInt(strings.TrimSpace(parts[i]), 10, 64)
		if err == nil {
			nums[i] = n
		}
	}
	suffix := ""
	if len(parts) > 3 {
		suffix = parts[3]
	}
	return nums, suffix
}
