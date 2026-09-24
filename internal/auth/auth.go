package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

type Session struct {
	Username string
	Admin    bool
	Expires  time.Time
}

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	ttl      time.Duration
}

func NewManager(ttl time.Duration) *Manager {
	m := &Manager{
		sessions: make(map[string]*Session),
		ttl:      ttl,
	}
	go m.gcLoop()
	return m
}

func (m *Manager) Create(username string, admin bool) string {
	token := randomToken(32)
	m.mu.Lock()
	m.sessions[token] = &Session{
		Username: username,
		Admin:    admin,
		Expires:  time.Now().Add(m.ttl),
	}
	m.mu.Unlock()
	return token
}

func (m *Manager) Get(token string) (*Session, bool) {
	if token == "" {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	if !ok {
		return nil, false
	}
	if time.Now().After(s.Expires) {
		delete(m.sessions, token)
		return nil, false
	}
	s.Expires = time.Now().Add(m.ttl)
	return s, true
}

func (m *Manager) Delete(token string) {
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

func (m *Manager) gcLoop() {
	t := time.NewTicker(time.Minute)
	for range t.C {
		m.mu.Lock()
		now := time.Now()
		for k, s := range m.sessions {
			if now.After(s.Expires) {
				delete(m.sessions, k)
			}
		}
		m.mu.Unlock()
	}
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
