// Package backup reads official Venera ".venera" backups and turns them into
// structured data a client can restore. See docs/VENERA-BACKUP.md.
//
// Core never writes the client's databases and never executes source scripts
// while restoring: it hands back data and script text, and the client applies
// what the user selected.
package backup

// DartStringHash returns String.hashCode as computed by the native Dart VM
// for s. Venera stores this value, not the source key, in the "type" column
// of history and favourite rows. The algorithm follows the Dart SDK
// (runtime/vm/object.h StringHasher and runtime/vm/hash.h): one combine step
// per UTF-16 code unit, a finalisation step, a 30-bit mask, and 0 mapped to 1.
//
// Dart compiled to JavaScript hashes strings differently; Venera does not
// ship a web build, so only the VM variant matters.
func DartStringHash(s string) int64 {
	var h uint32
	for _, u := range utf16Units(s) {
		h += uint32(u)
		h += h << 10
		h ^= h >> 6
	}
	h += h << 3
	h ^= h >> 11
	h += h << 15
	h &= 1<<30 - 1
	if h == 0 {
		h = 1
	}
	return int64(h)
}

// utf16Units encodes s as UTF-16, the code units Dart strings are made of.
func utf16Units(s string) []uint16 {
	out := make([]uint16, 0, len(s))
	for _, r := range s {
		switch {
		case r < 0x10000:
			out = append(out, uint16(r))
		default:
			r -= 0x10000
			out = append(out, uint16(0xd800+(r>>10)), uint16(0xdc00+(r&0x3ff)))
		}
	}
	return out
}

// LocalType is the "type" value of local comics.
const LocalType int64 = 0

// LegacyBuiltinKeys are source keys that old Venera and PicaComic data may
// reference even when no script for them is in the backup.
var LegacyBuiltinKeys = []string{"picacg", "ehentai", "jm", "hitomi", "wnacg", "nhentai"}

// TypeResolver maps "type" hashes back to source keys.
type TypeResolver struct {
	byHash map[int64][]string
}

// NewTypeResolver builds a resolver from candidate keys. Duplicate keys are
// ignored. Two different keys with the same hash are both kept so the
// collision can be reported instead of silently picking one.
func NewTypeResolver(keys ...[]string) *TypeResolver {
	r := &TypeResolver{byHash: map[int64][]string{}}
	seen := map[string]bool{}
	for _, list := range keys {
		for _, k := range list {
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			h := DartStringHash(k)
			r.byHash[h] = append(r.byHash[h], k)
		}
	}
	return r
}

// Resolve returns the source key for a "type" value. ok is false when the
// value is unknown or ambiguous; candidates then lists the colliding keys.
func (r *TypeResolver) Resolve(t int64) (key string, ok bool, candidates []string) {
	if t == LocalType {
		return "local", true, nil
	}
	ks := r.byHash[t]
	switch len(ks) {
	case 1:
		return ks[0], true, nil
	case 0:
		return "", false, nil
	}
	return "", false, append([]string(nil), ks...)
}
