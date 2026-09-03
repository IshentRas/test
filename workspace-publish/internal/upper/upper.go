package upper

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Change is one path relative to the repo root derived from upperdir.
type Change struct {
	// RelPath is slash-separated path relative to the overlay root (no leading /).
	RelPath string
	// Delete is true for whiteouts (base file should be removed in the commit).
	Delete bool
	// AbsPath is the source file in upperdir for non-delete changes.
	AbsPath string
}

// Walk enumerates COW delta entries under upperdir.
// Detects OverlayFS whiteouts as character devices and legacy .wh.* names.
func Walk(upperdir string) ([]Change, error) {
	var out []Change
	err := filepath.WalkDir(upperdir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(upperdir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Overlay workdir marker dirs / opaque xattrs are out of scope for v0.
		name := d.Name()
		if strings.HasPrefix(name, ".") && name != ".wh." && !strings.HasPrefix(name, ".wh.") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		relSlash := filepath.ToSlash(rel)

		if strings.HasPrefix(name, ".wh.") {
			target := strings.TrimPrefix(name, ".wh.")
			parent := filepath.ToSlash(filepath.Dir(rel))
			if parent == "." {
				parent = ""
			}
			del := target
			if parent != "" {
				del = parent + "/" + target
			}
			out = append(out, Change{RelPath: del, Delete: true})
			return nil
		}

		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if isWhiteout(info) {
			out = append(out, Change{RelPath: relSlash, Delete: true})
			return nil
		}

		out = append(out, Change{RelPath: relSlash, AbsPath: path})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk upperdir %s: %w", upperdir, err)
	}
	return out, nil
}

func isWhiteout(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	// Overlay whiteout: character device with rdev 0/0.
	return info.Mode()&os.ModeCharDevice != 0 && st.Rdev == 0
}
