// Package offline provides local video offline caching: task planning for HLS
// and direct media streams, transport-mediated downloads, resume support,
// SHA-256 integrity verification, local M3U8 playlist rewriting, and cache
// cleanup.
//
// Core never opens direct network connections; every remote byte flows through
// host.Transport.
package offline

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
)

var (
	ErrInvalidTaskID   = errors.New("offline: invalid task id")
	ErrPathTraversal   = errors.New("offline: path escapes cache root")
	ErrTaskNotFound    = errors.New("offline: task not found")
	ErrIntegrityFailed = errors.New("offline: integrity verification failed")
)

var (
	taskIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	tempCounter   atomic.Uint64
)

var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// ValidateTaskID verifies that id is a safe single path component across
// POSIX and Windows filesystems.
func ValidateTaskID(id string) error {
	if id == "" || id == "." || id == ".." {
		return ErrInvalidTaskID
	}
	if strings.ContainsAny(id, `/\:`+"\x00") || strings.Contains(id, "..") {
		return ErrInvalidTaskID
	}
	if strings.HasSuffix(id, ".") || strings.HasSuffix(id, " ") {
		return ErrInvalidTaskID
	}
	if !taskIDPattern.MatchString(id) {
		return ErrInvalidTaskID
	}
	base := id
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	if reservedNames[strings.ToUpper(base)] {
		return ErrInvalidTaskID
	}
	return nil
}

// SafeTaskDir resolves and validates the directory for taskID under rootDir,
// preventing path traversal and symlink escapes.
func SafeTaskDir(rootDir, taskID string) (string, error) {
	if strings.TrimSpace(rootDir) == "" {
		return "", errors.New("offline: root directory is required")
	}
	if strings.ContainsRune(rootDir, 0) {
		return "", ErrPathTraversal
	}
	if err := ValidateTaskID(taskID); err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(filepath.Clean(rootDir))
	if err != nil {
		return "", fmt.Errorf("offline: resolve root directory: %w", err)
	}
	target := filepath.Join(absRoot, taskID)
	if err := ensureWithin(absRoot, target); err != nil {
		return "", err
	}
	if err := checkNoSymlinkEscape(absRoot, target); err != nil {
		return "", err
	}
	return target, nil
}

// SafeFilePath resolves a slash-separated relative path strictly inside baseDir.
func SafeFilePath(baseDir, relPath string) (string, error) {
	if strings.TrimSpace(baseDir) == "" {
		return "", errors.New("offline: base directory is required")
	}
	if relPath == "" || strings.ContainsAny(relPath, `\:`+"\x00") {
		return "", ErrPathTraversal
	}
	if strings.HasPrefix(relPath, "/") {
		return "", ErrPathTraversal
	}
	cleaned := path.Clean(relPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", ErrPathTraversal
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") {
			return "", ErrPathTraversal
		}
		base := seg
		if dot := strings.IndexByte(base, '.'); dot >= 0 {
			base = base[:dot]
		}
		if reservedNames[strings.ToUpper(base)] {
			return "", ErrPathTraversal
		}
	}
	absBase, err := filepath.Abs(filepath.Clean(baseDir))
	if err != nil {
		return "", fmt.Errorf("offline: resolve base directory: %w", err)
	}
	target := filepath.Join(absBase, filepath.FromSlash(cleaned))
	if err := ensureWithin(absBase, target); err != nil {
		return "", err
	}
	if err := checkNoSymlinkEscape(absBase, target); err != nil {
		return "", err
	}
	return target, nil
}

func ensureWithin(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return ErrPathTraversal
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ErrPathTraversal
	}
	return nil
}

// checkNoSymlinkEscape verifies that existing path segments from root down to
// target are not symlinks escaping root.
func checkNoSymlinkEscape(root, target string) error {
	realRoot := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		realRoot = r
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	rel, err := filepath.Rel(root, target)
	if err != nil {
		return ErrPathTraversal
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return ErrPathTraversal
		}
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if err := ensureWithin(realRoot, resolved); err != nil {
				return ErrPathTraversal
			}
		}
	}
	return nil
}

// atomicWriteFile writes data to a unique temp file in the destination
// directory and renames it into place. On any failure the file handle is
// closed and the temporary file is removed.
func atomicWriteFile(dest string, data []byte) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	seq := tempCounter.Add(1)
	tmp := fmt.Sprintf("%s.tmp.%d", dest, seq)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}
	if _, err := f.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func fileSHA256AndSize(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func bytesSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func removeTempFiles(dir string) int64 {
	var freed int64
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.Contains(name, ".tmp.") || strings.HasSuffix(name, ".tmp") {
			if info, statErr := d.Info(); statErr == nil {
				freed += info.Size()
			}
			_ = os.Remove(p)
		}
		return nil
	})
	return freed
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, statErr := d.Info(); statErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
