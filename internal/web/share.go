package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fileshare/internal/config"
	"fileshare/internal/vfs"
)

func shareCookieName(token string) string {
	return "fs_share_" + token
}

func shareCookieValue(sh config.Share) string {
	sum := sha256.Sum256([]byte(sh.PasswordHash))
	return hex.EncodeToString(sum[:8])
}

func (s *Server) shareByToken(r *http.Request) (config.Share, int) {
	token := r.PathValue("token")
	sh, ok := s.shares.Get(token)
	if !ok {
		return config.Share{}, http.StatusNotFound
	}
	if s.shares.Expired(sh) {
		return config.Share{}, http.StatusGone
	}
	return sh, 0
}

func (s *Server) shareAuthorized(r *http.Request, sh config.Share) bool {
	if sh.PasswordHash == "" {
		return true
	}
	if c, err := r.Cookie(shareCookieName(sh.Token)); err == nil && c.Value == shareCookieValue(sh) {
		return true
	}
	if p := r.Header.Get("X-Share-Password"); p != "" && s.shares.CheckPassword(sh, p) {
		return true
	}
	if p := r.URL.Query().Get("password"); p != "" && s.shares.CheckPassword(sh, p) {
		return true
	}
	return false
}

func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request) {
	_, code := s.shareByToken(r)
	if code != 0 {
		http.Error(w, http.StatusText(code), code)
		return
	}
	serveStaticHTML(w, "share.html")
}

func (s *Server) handleShareAuth(w http.ResponseWriter, r *http.Request) {
	sh, code := s.shareByToken(r)
	if code != 0 {
		writeJSON(w, code, map[string]string{"error": http.StatusText(code)})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
		return
	}
	if !s.shares.CheckPassword(sh, req.Password) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "неверный пароль"})
		return
	}
	http.SetCookie(w, newCookie(shareCookieName(sh.Token), shareCookieValue(sh), false, 3600*24*30))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) requireShareAccess(w http.ResponseWriter, r *http.Request) (config.Share, bool) {
	sh, code := s.shareByToken(r)
	if code != 0 {
		writeJSON(w, code, map[string]string{"error": http.StatusText(code)})
		return sh, false
	}
	if !s.shareAuthorized(r, sh) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"error": "нужен пароль", "auth_required": true})
		return sh, false
	}
	return sh, true
}

func (s *Server) handleShareList(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.requireShareAccess(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	abs, err := s.shares.ResolveSharePath(sh, rel)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "доступ запрещён"})
		return
	}
	fi, err := os.Stat(abs)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "не найдено"})
		return
	}
	if !fi.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не папка"})
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer f.Close()
	infos, err := f.Readdir(-1)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].IsDir() != infos[j].IsDir() {
			return infos[i].IsDir()
		}
		return strings.ToLower(infos[i].Name()) < strings.ToLower(infos[j].Name())
	})
	entries := make([]entryJSON, 0, len(infos))
	for _, info := range infos {
		entries = append(entries, entryJSON{
			Name:    info.Name(),
			Path:    filepath.ToSlash(filepath.Join(rel, info.Name())),
			IsDir:   info.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format("02.01.2006 15:04"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":    sh.Name,
		"path":    filepath.ToSlash(rel),
		"parent":  parentPath(rel),
		"entries": entries,
	})
}

func (s *Server) handleShareSize(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.requireShareAccess(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	abs, err := s.shares.ResolveSharePath(sh, rel)
	if err != nil || !fileExists(abs) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "не найдено"})
		return
	}
	size, err := dirSizeAll(abs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"size": size, "limit": s.store.Get().ZipMaxBytes})
}

func (s *Server) handleShareFile(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.requireShareAccess(w, r)
	if !ok {
		return
	}
	rel := r.PathValue("path")
	abs, err := s.shares.ResolveSharePath(sh, rel)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "доступ запрещён"})
		return
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "файл не найден"})
		return
	}
	w.Header().Set("Content-Disposition", disposition(filepath.Base(abs)))
	http.ServeFile(w, r, abs)
}

func (s *Server) handleShareZip(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.requireShareAccess(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	abs, err := s.shares.ResolveSharePath(sh, rel)
	if err != nil || !fileExists(abs) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "не найдено"})
		return
	}
	name := filepath.Base(abs)
	if rel == "" || rel == "." {
		name = sh.Name
	}
	err = streamZip(w, name, abs, s.store.Get().ZipMaxBytes)
	if err == errTooBig {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
			"error": "папка больше лимита ZIP — используйте загрузчик или скачивание по файлам",
		})
		return
	}
	if err != nil {
		s.logger.Printf("zip share %s: %v", rel, err)
	}
}

func (s *Server) handleShareManifest(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.requireShareAccess(w, r)
	if !ok {
		return
	}
	withHash := true
	if hq := r.URL.Query().Get("hash"); hq == "0" || hq == "false" {
		withHash = false
	}
	rel := r.URL.Query().Get("path")
	abs, err := s.shares.ResolveSharePath(sh, rel)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "не найдено"})
		return
	}
	name := sh.Name
	if vfs.CleanVirtual(rel) != "" {
		name = filepath.Base(abs)
	}
	m, err := s.shares.ManifestPath(abs, name, withHash)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, m)
}
