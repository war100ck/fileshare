package vfs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fileshare/internal/config"
)

func CleanVirtual(rel string) string {
	rel = filepath.ToSlash(rel)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "." {
		return ""
	}
	return strings.Trim(rel, "/")
}

func Resolve(mounts []config.Mount, legacyRoot, rel string) (string, error) {
	rel = CleanVirtual(rel)
	if len(mounts) == 0 {
		return resolveLegacy(legacyRoot, rel)
	}
	if rel == "" {
		return "", fmt.Errorf("корневой уровень: список папок")
	}
	first, rest := splitFirst(rel)
	absMount := ""
	for _, m := range mounts {
		if strings.EqualFold(m.Name, first) {
			absMount = m.Path
			break
		}
	}
	if absMount == "" {
		return "", os.ErrNotExist
	}
	absMount = filepath.Clean(absMount)
	abs := filepath.Join(absMount, rest)
	r, err := filepath.Rel(absMount, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	return abs, nil
}

func resolveLegacy(root, rel string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	relClean := filepath.Clean(filepath.FromSlash(rel))
	if vol := filepath.VolumeName(relClean); vol != "" {
		relClean = strings.TrimPrefix(relClean, vol)
	}
	if filepath.IsAbs(relClean) {
		relClean = strings.TrimLeft(relClean, `/\`)
	}
	if relClean == "." {
		relClean = ""
	}
	abs := filepath.Join(absRoot, relClean)
	r, err := filepath.Rel(absRoot, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	return abs, nil
}

func IsWithin(mounts []config.Mount, legacyRoot, abs string) bool {
	abs = filepath.Clean(abs)
	if len(mounts) == 0 {
		return withinOne(legacyRoot, abs)
	}
	for _, m := range mounts {
		if withinOne(m.Path, abs) {
			return true
		}
	}
	return false
}

func withinOne(root, abs string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	r, err := filepath.Rel(absRoot, abs)
	if err != nil {
		return false
	}
	return r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

func splitFirst(rel string) (string, string) {
	if i := strings.Index(rel, "/"); i >= 0 {
		return rel[:i], rel[i+1:]
	}
	return rel, ""
}

func LegacySeed(mounts []config.Mount, legacyRoot string) (config.Mount, error) {
	abs, err := filepath.Abs(legacyRoot)
	if err != nil {
		return config.Mount{}, err
	}
	name := filepath.Base(abs)
	if name == "" || name == "." || strings.ContainsAny(name, `/\`) {
		name = "shared"
	}
	m := config.Mount{Name: name, Path: abs}
	for _, ex := range mounts {
		if strings.EqualFold(ex.Name, m.Name) {
			return config.Mount{}, fmt.Errorf("папка с именем %q уже есть", m.Name)
		}
	}
	return m, nil
}

func Drives() []string {
	var out []string
	for c := 'A'; c <= 'Z'; c++ {
		d := string(c) + `:\`
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			out = append(out, d)
		}
	}
	return out
}

func NormalizeBrowse(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	p := filepath.Clean(path)
	if filepath.IsAbs(p) {
		return p, nil
	}
	return "", fmt.Errorf("нужен абсолютный путь")
}

func BrowseParent(path string) string {
	if path == "" {
		return ""
	}
	vol := filepath.VolumeName(path)
	parent := filepath.Dir(path)
	if vol != "" && (parent == vol || parent == vol+string(filepath.Separator) || filepath.Clean(parent) == filepath.Clean(vol)) {
		return ""
	}
	if filepath.Clean(path) == filepath.Clean(vol+string(filepath.Separator)) {
		return ""
	}
	return parent
}
