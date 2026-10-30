package backup

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
)

// Limits bound what Open reads from an untrusted archive.
type Limits struct {
	MaxEntries     int   // default 20000
	MaxEntryBytes  int64 // default 512 MiB (databases can be large)
	MaxTotalBytes  int64 // default 2 GiB
	MaxSourceBytes int64 // per comic_source file, default 16 MiB
}

func (l Limits) withDefaults() Limits {
	if l.MaxEntries <= 0 {
		l.MaxEntries = 20000
	}
	if l.MaxEntryBytes <= 0 {
		l.MaxEntryBytes = 512 << 20
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = 2 << 30
	}
	if l.MaxSourceBytes <= 0 {
		l.MaxSourceBytes = 16 << 20
	}
	return l
}

// Known member names.
const (
	MemberHistory   = "history.db"
	MemberFavorites = "local_favorite.db"
	MemberAppData   = "appdata.json"
	MemberCookies   = "cookie.db"
	SourceDir       = "comic_source/"
)

// ErrInvalidArchive means the file is not a usable .venera backup.
var ErrInvalidArchive = errors.New("backup: not a valid .venera archive")

// Archive is an opened backup. Members are read lazily; nothing is extracted
// to disk except database files, which SQLite needs as files.
type Archive struct {
	zr      *zip.Reader
	limits  Limits
	members map[string]*zip.File
	// Unknown lists members Core does not interpret. They are kept in the
	// original archive and must be shown to the user, not dropped silently.
	Unknown []string
	// Sources lists comic_source members by file name.
	Sources []string
	// read counts decompressed bytes (see charge); mu guards it so one
	// Archive can be read from several goroutines.
	mu   sync.Mutex
	read int64
}

// Open validates the central directory of a .venera archive.
func Open(r io.ReaderAt, size int64, limits Limits) (*Archive, error) {
	limits = limits.withDefaults()
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArchive, err)
	}
	if len(zr.File) > limits.MaxEntries {
		return nil, fmt.Errorf("%w: more than %d entries", ErrInvalidArchive, limits.MaxEntries)
	}
	a := &Archive{zr: zr, limits: limits, members: map[string]*zip.File{}}
	var total uint64
	for _, f := range zr.File {
		name, err := cleanName(f.Name)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidArchive, err)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if _, dup := a.members[name]; dup {
			return nil, fmt.Errorf("%w: duplicate entry %q", ErrInvalidArchive, name)
		}
		if f.UncompressedSize64 > uint64(limits.MaxEntryBytes) {
			return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrInvalidArchive, name, limits.MaxEntryBytes)
		}
		total += f.UncompressedSize64
		if total > uint64(limits.MaxTotalBytes) {
			return nil, fmt.Errorf("%w: archive expands beyond %d bytes", ErrInvalidArchive, limits.MaxTotalBytes)
		}
		a.members[name] = f
		switch {
		case name == MemberHistory, name == MemberFavorites, name == MemberAppData, name == MemberCookies:
		case strings.HasPrefix(name, SourceDir) && !strings.Contains(name[len(SourceDir):], "/"):
			if f.UncompressedSize64 > uint64(limits.MaxSourceBytes) {
				return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrInvalidArchive, name, limits.MaxSourceBytes)
			}
			a.Sources = append(a.Sources, name[len(SourceDir):])
		default:
			a.Unknown = append(a.Unknown, name)
		}
	}
	if !a.Has(MemberHistory) && !a.Has(MemberFavorites) && !a.Has(MemberAppData) && len(a.Sources) == 0 {
		return nil, fmt.Errorf("%w: no Venera data found", ErrInvalidArchive)
	}
	sort.Strings(a.Sources)
	sort.Strings(a.Unknown)
	return a, nil
}

// cleanName rejects absolute paths, parent references, backslashes and
// NUL bytes, and returns the slash-separated name.
func cleanName(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, `\`) {
		return "", fmt.Errorf("invalid entry name %q", name)
	}
	if strings.HasPrefix(name, "/") || (len(name) > 1 && name[1] == ':') {
		return "", fmt.Errorf("absolute entry name %q", name)
	}
	c := path.Clean(name)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || strings.HasPrefix(c, "/") {
		return "", fmt.Errorf("entry %q leaves the archive", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("entry %q contains a parent reference", name)
		}
	}
	if strings.HasSuffix(name, "/") {
		return c + "/", nil
	}
	return c, nil
}

// Has reports whether a member exists.
func (a *Archive) Has(name string) bool { _, ok := a.members[name]; return ok }

// Member returns a member's bytes, enforcing the size limit while reading so a
// lying header cannot expand past it.
func (a *Archive) Member(name string) ([]byte, error) {
	f, ok := a.members[name]
	if !ok {
		return nil, fmt.Errorf("backup: no member %q", name)
	}
	limit := a.limits.MaxEntryBytes
	if strings.HasPrefix(name, SourceDir) {
		limit = a.limits.MaxSourceBytes
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("backup: reading %s: %w", name, err)
	}
	if n > limit {
		return nil, fmt.Errorf("backup: %s is larger than %d bytes", name, limit)
	}
	if err := a.charge(n); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// extract streams a member to a new file at path, enforcing the entry and
// total limits on the bytes actually decompressed.
func (a *Archive) extract(name, path string) error {
	f, ok := a.members[name]
	if !ok {
		return fmt.Errorf("backup: no member %q", name)
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(rc, a.limits.MaxEntryBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("backup: extracting %s: %w", name, err)
	}
	if n > a.limits.MaxEntryBytes {
		return fmt.Errorf("backup: %s is larger than %d bytes", name, a.limits.MaxEntryBytes)
	}
	return a.charge(n)
}

// charge counts decompressed bytes against MaxTotalBytes. The central
// directory sizes were checked in Open, but they can lie; this counts what
// the decompressor really produced.
func (a *Archive) charge(n int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.read += n
	if a.read > a.limits.MaxTotalBytes {
		return fmt.Errorf("%w: archive expands beyond %d bytes", ErrInvalidArchive, a.limits.MaxTotalBytes)
	}
	return nil
}

// SourceKeys returns keys named by comic_source files ("<key>.js",
// "<key>.data"), used to resolve "type" hashes.
func (a *Archive) SourceKeys() []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range a.Sources {
		k := strings.TrimSuffix(strings.TrimSuffix(n, ".js"), ".data")
		if k != n && k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
