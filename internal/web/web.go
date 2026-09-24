package web

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"fileshare/internal/auth"
	"fileshare/internal/config"
	"fileshare/internal/sftpserver"
	"fileshare/internal/shares"
)

//go:embed static/*
var staticFS embed.FS

const (
	cookieSession = "fs_sid"
)

type Server struct {
	store   *config.Store
	auth    *auth.Manager
	shares  *shares.Store
	limiter *auth.LoginLimiter
	logger  *log.Logger
	sftp    *sftpserver.Server

	mu        sync.Mutex
	ln        net.Listener
	httpSrv   *http.Server
	baseURL   string
	extIP     string
	prevBind  string
	prevPort  int
	prevHTTPS bool
}

func New(store *config.Store, authMgr *auth.Manager, shareStore *shares.Store, sftpSrv *sftpserver.Server, logger *log.Logger) *Server {
	return &Server{
		store:   store,
		auth:    authMgr,
		shares:  shareStore,
		limiter: auth.NewLoginLimiter(),
		logger:  logger,
		sftp:    sftpSrv,
	}
}

func (s *Server) Start(bind string, port int, https bool) error {
	if err := s.startAttempt(bind, port, https); err != nil {
		s.mu.Lock()
		pb, pp, ph := s.prevBind, s.prevPort, s.prevHTTPS
		s.mu.Unlock()
		if pb != "" && !(pb == bind && pp == port && ph == https) {
			if err2 := s.startAttempt(pb, pp, ph); err2 != nil {
				s.logger.Printf("web: откат настройки тоже не удался: %v", err2)
			}
		}
		return err
	}
	return nil
}

func (s *Server) startAttempt(bind string, port int, https bool) error {
	mux := http.NewServeMux()
	s.routes(mux)

	s.mu.Lock()
	prevLn := s.ln
	prevSrv := s.httpSrv
	s.ln = nil
	s.httpSrv = nil
	s.mu.Unlock()
	if prevLn != nil {
		prevLn.Close()
	}

	addr := fmt.Sprintf("%s:%d", bind, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}
	if https {
		cfg := s.store.Get()
		cert, key, err := ensureCert(cfg.TLSCert, cfg.TLSKey, cfg.ExternalIP)
		if err != nil {
			ln.Close()
			return fmt.Errorf("TLS: %w", err)
		}
		tlsCert, err := tlsLoad(cert, key)
		if err != nil {
			ln.Close()
			return err
		}
		srv.TLSConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{tlsCert},
		}
		go func() {
			if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				s.logger.Printf("web: %v", err)
			}
		}()
	} else {
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				s.logger.Printf("web: %v", err)
			}
		}()
	}

	s.mu.Lock()
	s.ln = ln
	s.httpSrv = srv
	s.prevBind = bind
	s.prevPort = port
	s.prevHTTPS = https
	s.mu.Unlock()
	if prevSrv != nil {
		go func() {
			time.Sleep(300 * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			prevSrv.Shutdown(ctx)
			cancel()
		}()
	}
	scheme := "http"
	if https {
		scheme = "https"
	}
	s.baseURL = fmt.Sprintf("%s://%s", scheme, addr)
	if bind == "0.0.0.0" || bind == "::" {
		s.baseURL = fmt.Sprintf("%s://127.0.0.1:%d", scheme, port)
	}
	s.logger.Printf("Веб-интерфейс: %s", s.baseURL)
	return nil
}

func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := s.httpSrv.Shutdown(ctx)
		s.httpSrv = nil
		s.ln = nil
		return err
	}
	return nil
}

func (s *Server) BaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseURL
}

func (s *Server) routes(mux *http.ServeMux) {
	staticSub, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(staticSub))
	mux.Handle("GET /static/", http.StripPrefix("/static/", fileServer))

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		serveIndex(w)
	})

	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.requireAuth(s.handleMe))
	mux.HandleFunc("POST /api/password", s.requireAuth(s.handleChangePassword))

	mux.HandleFunc("GET /api/fs", s.requireAuth(s.handleFsList))
	mux.HandleFunc("GET /api/size", s.requireAuth(s.handleDirSize))
	mux.HandleFunc("POST /api/mkdir", s.requireAuth(s.handleMkdir))
	mux.HandleFunc("POST /api/rename", s.requireAuth(s.handleRename))
	mux.HandleFunc("POST /api/delete", s.requireAuth(s.handleDelete))
	mux.HandleFunc("POST /api/upload", s.requireAuth(s.handleUpload))
	mux.HandleFunc("GET /download", s.requireAuth(s.handleDownload))
	mux.HandleFunc("GET /zip", s.requireAuth(s.handleZip))
	mux.HandleFunc("GET /api/manifest", s.requireAuth(s.handleAuthedManifest))
	mux.HandleFunc("GET /dl", s.handleExe)

	mux.HandleFunc("GET /api/shares", s.requireAuth(s.handleSharesList))
	mux.HandleFunc("POST /api/shares", s.requireAuth(s.handleSharesCreate))
	mux.HandleFunc("DELETE /api/shares", s.requireAuth(s.handleSharesDelete))

	mux.HandleFunc("GET /api/users", s.requireAdmin(s.handleUsersList))
	mux.HandleFunc("POST /api/users", s.requireAdmin(s.handleUsersCreate))
	mux.HandleFunc("DELETE /api/users", s.requireAdmin(s.handleUsersDelete))

	mux.HandleFunc("GET /api/settings", s.requireAdmin(s.handleSettingsGet))
	mux.HandleFunc("PUT /api/settings", s.requireAdmin(s.handleSettingsPut))
	mux.HandleFunc("GET /api/netinfo", s.requireAdmin(s.handleNetInfo))
	mux.HandleFunc("GET /api/base", s.handlePublicBase)
	mux.HandleFunc("GET /api/mounts", s.requireAdmin(s.handleMountsList))
	mux.HandleFunc("POST /api/mounts", s.requireAdmin(s.handleMountsCreate))
	mux.HandleFunc("DELETE /api/mounts", s.requireAdmin(s.handleMountsDelete))
	mux.HandleFunc("GET /api/browse", s.requireAdmin(s.handleBrowse))

	mux.HandleFunc("GET /s/{token}", s.handleSharePage)
	mux.HandleFunc("POST /s/{token}/auth", s.handleShareAuth)
	mux.HandleFunc("GET /s/{token}/list", s.handleShareList)
	mux.HandleFunc("GET /s/{token}/size", s.handleShareSize)
	mux.HandleFunc("GET /s/{token}/file/{path...}", s.handleShareFile)
	mux.HandleFunc("GET /s/{token}/zip", s.handleShareZip)
	mux.HandleFunc("GET /s/{token}/manifest.json", s.handleShareManifest)
}

func serveIndex(w http.ResponseWriter) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func serveStaticHTML(w http.ResponseWriter, name string) {
	data, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

type ctxKey int

const ctxSessionKey ctxKey = iota

func (s *Server) sessionFrom(r *http.Request) (*auth.Session, bool) {
	c, err := r.Cookie(cookieSession)
	if err != nil {
		return nil, false
	}
	return s.auth.Get(c.Value)
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.sessionFrom(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "не авторизован"})
			return
		}
		ctx := context.WithValue(r.Context(), ctxSessionKey, sess)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		sess := r.Context().Value(ctxSessionKey).(*auth.Session)
		if !sess.Admin {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "нужны права администратора"})
			return
		}
		next(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v interface{}) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func newCookie(name, value string, secure bool, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   maxAge,
	}
}

func randomTokenStr(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func disposition(name string) string {
	name = strings.ReplaceAll(name, `"`, "")
	return fmt.Sprintf(`attachment; filename*=UTF-8''%s`, urlEncode(name))
}

func urlEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("-_.~", rune(c)) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func resolveInRoot(root, rel string) (string, error) {
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func parseBoolQuery(r *http.Request, name string) bool {
	v := r.URL.Query().Get(name)
	b, _ := strconv.ParseBool(v)
	return b
}
