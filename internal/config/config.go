package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	Admin        bool   `json:"admin"`
}

type Share struct {
	Token        string `json:"token"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	PasswordHash string `json:"password_hash,omitempty"`
	Expires      string `json:"expires,omitempty"`
	Created      string `json:"created"`
}

type Mount struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Config struct {
	WebPort           int     `json:"web_port"`
	SFTPPort          int     `json:"sftp_port"`
	FTPPort           int     `json:"ftp_port"`
	Bind              string  `json:"bind"`
	RootDir           string  `json:"root_dir"`
	Mounts            []Mount `json:"mounts"`
	HTTPS             bool    `json:"https"`
	TLSCert           string  `json:"tls_cert"`
	TLSKey            string  `json:"tls_key"`
	ZipMaxBytes       int64   `json:"zip_max_bytes"`
	ExternalIP        string  `json:"external_ip"`
	SessionTTLMinutes int     `json:"session_ttl_minutes"`
	Users             []User  `json:"users"`
	Shares            []Share `json:"shares"`
}

type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

func CheckHash(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func defaultConfig() Config {
	hash, _ := HashPassword("admin")
	return Config{
		WebPort:           8080,
		SFTPPort:          2222,
		FTPPort:           21,
		Bind:              "0.0.0.0",
		RootDir:           "shared",
		HTTPS:             false,
		TLSCert:           "",
		TLSKey:            "",
		ZipMaxBytes:       2 << 30,
		ExternalIP:        "",
		SessionTTLMinutes: 720,
		Users: []User{
			{Username: "admin", PasswordHash: hash, Admin: true},
		},
	}
}

func Open(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.cfg = defaultConfig()
		if err := s.Save(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := unmarshal(data, &s.cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	s.applyDefaults()
	s.migrateSharePaths()
	return s, nil
}

func (s *Store) applyDefaults() {
	d := defaultConfig()
	if s.cfg.WebPort == 0 {
		s.cfg.WebPort = d.WebPort
	}
	if s.cfg.SFTPPort == 0 {
		s.cfg.SFTPPort = d.SFTPPort
	}
	if s.cfg.FTPPort == 0 {
		s.cfg.FTPPort = d.FTPPort
	}
	if s.cfg.Bind == "" {
		s.cfg.Bind = d.Bind
	}
	if s.cfg.RootDir == "" {
		s.cfg.RootDir = d.RootDir
	}
	if s.cfg.ZipMaxBytes <= 0 {
		s.cfg.ZipMaxBytes = d.ZipMaxBytes
	}
	if s.cfg.SessionTTLMinutes <= 0 {
		s.cfg.SessionTTLMinutes = d.SessionTTLMinutes
	}
	for i := range s.cfg.Mounts {
		if s.cfg.Mounts[i].Path != "" {
			if abs, err := filepath.Abs(s.cfg.Mounts[i].Path); err == nil {
				s.cfg.Mounts[i].Path = abs
			}
		}
	}
}

func (s *Store) migrateSharePaths() {
	changed := false
	absRoot, err := filepath.Abs(s.cfg.RootDir)
	if err != nil {
		return
	}
	for i := range s.cfg.Shares {
		p := s.cfg.Shares[i].Path
		if p == "" || filepath.IsAbs(p) {
			continue
		}
		joined := filepath.Join(absRoot, filepath.FromSlash(p))
		if abs, err := filepath.Abs(joined); err == nil {
			s.cfg.Shares[i].Path = abs
			changed = true
		}
	}
	if changed {
		_ = s.Save()
	}
}

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Store) Update(fn func(*Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.cfg)
	return s.saveLocked()
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	data, err := marshalIndent(s.cfg)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) FindUser(username string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.cfg.Users {
		if u.Username == username {
			return u, true
		}
	}
	return User{}, false
}

func (s *Store) CheckPassword(username, password string) (User, bool) {
	u, ok := s.FindUser(username)
	if !ok {
		bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinva"), []byte(password))
		return User{}, false
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return User{}, false
	}
	return u, true
}

func EnsureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func HostKeyPath(dataDir string) string {
	return filepath.Join(dataDir, "host_key")
}

func LoadOrGenerateHostKey(dataDir string) (interface{}, error) {
	if err := EnsureDir(dataDir); err != nil {
		return nil, err
	}
	path := HostKeyPath(dataDir)
	if data, err := os.ReadFile(path); err == nil {
		der := data
		if block, _ := pem.Decode(data); block != nil {
			der = block.Bytes
		}
		key, err := x509.ParsePKCS8PrivateKey(der)
		if err != nil {
			return nil, fmt.Errorf("parse host key: %w", err)
		}
		return key, nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	return priv, nil
}

func ValidExpiry(s string) bool {
	if s == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return false
	}
	return t.After(time.Now())
}
