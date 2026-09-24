package shares

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fileshare/internal/config"
	"fileshare/internal/vfs"
)

type Store struct {
	cfg       *config.Store
	cachePath string

	hashMu    sync.Mutex
	hashCache map[string]string
}

type Manifest struct {
	Name      string         `json:"name"`
	Path      string         `json:"path"`
	Generated string         `json:"generated"`
	IsDir     bool           `json:"is_dir"`
	TotalSize int64          `json:"total_size"`
	Files     []ManifestFile `json:"files"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

func New(cfg *config.Store, dataDir string) (*Store, error) {
	s := &Store{
		cfg:       cfg,
		cachePath: filepath.Join(dataDir, "hashcache.json"),
		hashCache: make(map[string]string),
	}
	if data, err := os.ReadFile(s.cachePath); err == nil {
		_ = json.Unmarshal(data, &s.hashCache)
	}
	return s, nil
}

func NewToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Store) List() []config.Share {
	return s.cfg.Get().Shares
}

func (s *Store) Get(token string) (config.Share, bool) {
	for _, sh := range s.cfg.Get().Shares {
		if sh.Token == token {
			return sh, true
		}
	}
	return config.Share{}, false
}

func (s *Store) Create(path, name, password, expires string) (config.Share, error) {
	cfg := s.cfg.Get()
	abs, err := vfs.Resolve(cfg.Mounts, cfg.RootDir, path)
	if err != nil {
		return config.Share{}, fmt.Errorf("путь не найден")
	}
	if !vfs.IsWithin(cfg.Mounts, cfg.RootDir, abs) {
		return config.Share{}, fmt.Errorf("путь вне доступных папок")
	}
	if _, err := os.Stat(abs); err != nil {
		return config.Share{}, fmt.Errorf("путь не найден")
	}
	sh := config.Share{
		Token:   NewToken(),
		Name:    name,
		Path:    abs,
		Created: time.Now().Format(time.RFC3339),
	}
	if password != "" {
		h, err := config.HashPassword(password)
		if err != nil {
			return config.Share{}, err
		}
		sh.PasswordHash = h
	}
	if expires != "" {
		t, err := time.Parse(time.RFC3339, expires)
		if err != nil {
			return config.Share{}, fmt.Errorf("некорректная дата окончания")
		}
		sh.Expires = t.Format(time.RFC3339)
	}
	err = s.cfg.Update(func(c *config.Config) {
		c.Shares = append(c.Shares, sh)
	})
	if err != nil {
		return config.Share{}, err
	}
	return sh, nil
}

func (s *Store) Delete(token string) error {
	return s.cfg.Update(func(c *config.Config) {
		out := c.Shares[:0]
		for _, sh := range c.Shares {
			if sh.Token != token {
				out = append(out, sh)
			}
		}
		c.Shares = out
	})
}

func (s *Store) CheckPassword(sh config.Share, password string) bool {
	if sh.PasswordHash == "" {
		return true
	}
	return config.CheckHash(sh.PasswordHash, password)
}

func (s *Store) Expired(sh config.Share) bool {
	if sh.Expires == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, sh.Expires)
	if err != nil {
		return true
	}
	return time.Now().After(t)
}

func (s *Store) ResolveSharePath(sh config.Share, rel string) (string, error) {
	cfg := s.cfg.Get()
	base := filepath.Clean(sh.Path)
	if !vfs.IsWithin(cfg.Mounts, cfg.RootDir, base) {
		return "", os.ErrPermission
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
	abs := filepath.Join(base, relClean)
	r, err := filepath.Rel(base, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	return abs, nil
}

func (s *Store) Manifest(sh config.Share, withHash bool) (Manifest, error) {
	abs, err := s.ResolveSharePath(sh, "")
	if err != nil {
		return Manifest{}, err
	}
	return s.ManifestPath(abs, sh.Name, withHash)
}

func (s *Store) ManifestPath(abs, name string, withHash bool) (Manifest, error) {
	fi, err := os.Stat(abs)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{
		Name:      name,
		Path:      abs,
		Generated: time.Now().Format(time.RFC3339),
		IsDir:     fi.IsDir(),
	}
	if !fi.IsDir() {
		m.TotalSize = fi.Size()
		f := ManifestFile{Path: filepath.Base(abs), Size: fi.Size()}
		if withHash {
			f = s.cachedHash(abs, f.Path, fi, true)
		}
		m.Files = append(m.Files, f)
		return m, nil
	}
	baseRel := abs
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(baseRel, p)
		if err != nil {
			return err
		}
		m.TotalSize += info.Size()
		if withHash {
			m.Files = append(m.Files, s.cachedHash(p, filepath.ToSlash(rel), info, true))
		} else {
			m.Files = append(m.Files, ManifestFile{Path: filepath.ToSlash(rel), Size: info.Size()})
		}
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return m, nil
}

func (s *Store) cachedHash(absPath, rel string, fi os.FileInfo, compute bool) ManifestFile {
	mf := ManifestFile{Path: rel, Size: fi.Size()}
	if !compute {
		return mf
	}
	key := fmt.Sprintf("%s|%d|%d", absPath, fi.Size(), fi.ModTime().UnixNano())
	s.hashMu.Lock()
	h, ok := s.hashCache[key]
	s.hashMu.Unlock()
	if ok {
		mf.SHA256 = h
		return mf
	}
	sum, err := hashFile(absPath)
	if err != nil {
		return mf
	}
	mf.SHA256 = sum
	s.hashMu.Lock()
	s.hashCache[key] = sum
	data, _ := json.Marshal(s.hashCache)
	s.hashMu.Unlock()
	tmp := s.cachePath + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, s.cachePath)
	}
	return mf
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func DirSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
