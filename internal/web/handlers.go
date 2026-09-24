package web

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fileshare/internal/auth"
	"fileshare/internal/config"
	"fileshare/internal/vfs"
)

type entryJSON struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modtime"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.Allow(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "слишком много попыток, подождите 5 минут"})
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
		return
	}
	u, ok := s.store.CheckPassword(req.Username, req.Password)
	if !ok {
		s.limiter.Fail(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "неверный логин или пароль"})
		return
	}
	s.limiter.Success(ip)
	token := s.auth.Create(u.Username, u.Admin)
	cfg := s.store.Get()
	http.SetCookie(w, newCookie(cookieSession, token, cfg.HTTPS, cfg.SessionTTLMinutes*60))
	writeJSON(w, http.StatusOK, map[string]interface{}{"username": u.Username, "admin": u.Admin})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieSession); err == nil {
		s.auth.Delete(c.Value)
	}
	http.SetCookie(w, newCookie(cookieSession, "", false, -1))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := r.Context().Value(ctxSessionKey).(*auth.Session)
	writeJSON(w, http.StatusOK, map[string]interface{}{"username": sess.Username, "admin": sess.Admin})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	sess := r.Context().Value(ctxSessionKey).(*auth.Session)
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := readJSON(r, &req); err != nil || len(req.NewPassword) < 4 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нужен новый пароль от 4 символов"})
		return
	}
	if _, ok := s.store.CheckPassword(sess.Username, req.OldPassword); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "старый пароль неверен"})
		return
	}
	hash, err := config.HashPassword(req.NewPassword)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	err = s.store.Update(func(c *config.Config) {
		for i := range c.Users {
			if c.Users[i].Username == sess.Username {
				c.Users[i].PasswordHash = hash
			}
		}
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleFsList(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	cfg := s.store.Get()
	if vfs.CleanVirtual(rel) == "" && len(cfg.Mounts) > 0 {
		entries := make([]entryJSON, 0, len(cfg.Mounts))
		for _, m := range cfg.Mounts {
			mod := ""
			if fi, err := os.Stat(m.Path); err == nil {
				mod = fi.ModTime().Format("02.01.2006 15:04")
			}
			entries = append(entries, entryJSON{
				Name:    m.Name,
				Path:    m.Name,
				IsDir:   true,
				Size:    0,
				ModTime: mod,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"path":         "",
			"parent":       "",
			"virtual_root": true,
			"entries":      entries,
		})
		return
	}
	abs, err := vfs.Resolve(cfg.Mounts, cfg.RootDir, rel)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "не найдено"})
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
		"path":   filepath.ToSlash(rel),
		"parent": parentPath(rel),
		"entries": entries,
	})
}

func parentPath(p string) string {
	p = strings.Trim(p, "/")
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return ""
	}
	return p[:i]
}

func (s *Server) handleDirSize(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	cfg := s.store.Get()
	abs, err := vfs.Resolve(cfg.Mounts, cfg.RootDir, rel)
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

func dirSizeAll(path string) (int64, error) {
	var total int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &req); err != nil || req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "укажите путь"})
		return
	}
	cfg := s.store.Get()
	abs, err := vfs.Resolve(cfg.Mounts, cfg.RootDir, req.Path)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "доступ запрещён"})
		return
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
		return
	}
	root := s.store.Get()
	src, err := vfs.Resolve(root.Mounts, root.RootDir, req.From)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "доступ запрещён"})
		return
	}
	dst, err := vfs.Resolve(root.Mounts, root.RootDir, req.To)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "доступ запрещён"})
		return
	}
	if err := os.Rename(src, dst); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &req); err != nil || req.Path == "" || vfs.CleanVirtual(req.Path) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "укажите путь"})
		return
	}
	cfg := s.store.Get()
	abs, err := vfs.Resolve(cfg.Mounts, cfg.RootDir, req.Path)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "доступ запрещён"})
		return
	}
	if err := os.RemoveAll(abs); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	cfg := s.store.Get()
	abs, err := vfs.Resolve(cfg.Mounts, cfg.RootDir, dir)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "доступ запрещён"})
		return
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ошибка формы: " + err.Error()})
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "файлы не переданы"})
		return
	}
	saved := 0
	for _, fh := range files {
		name := filepath.Base(fh.Filename)
		if name == "." || name == string(filepath.Separator) || name == "" {
			continue
		}
		src, err := fh.Open()
		if err != nil {
			continue
		}
		dstPath := filepath.Join(abs, name)
		out, err := os.Create(dstPath)
		if err != nil {
			src.Close()
			continue
		}
		_, err = io.Copy(out, src)
		src.Close()
		out.Close()
		if err == nil {
			saved++
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "saved": saved})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	cfg := s.store.Get()
	abs, err := vfs.Resolve(cfg.Mounts, cfg.RootDir, rel)
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

func (s *Server) handleZip(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	cfg := s.store.Get()
	abs, err := vfs.Resolve(cfg.Mounts, cfg.RootDir, rel)
	if err != nil || !fileExists(abs) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "не найдено"})
		return
	}
	name := filepath.Base(abs)
	err = streamZip(w, name, abs, s.store.Get().ZipMaxBytes)
	if err == errTooBig {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
			"error": "папка больше лимита ZIP — используйте файловый менеджер или загрузчик",
		})
		return
	}
	if err != nil {
		s.logger.Printf("zip %s: %v", rel, err)
	}
}

func (s *Server) handleSharesList(w http.ResponseWriter, r *http.Request) {
	list := s.shares.List()
	out := make([]map[string]interface{}, 0, len(list))
	for _, sh := range list {
		out = append(out, map[string]interface{}{
			"token":        sh.Token,
			"name":         sh.Name,
			"path":         sh.Path,
			"has_password": sh.PasswordHash != "",
			"expires":      sh.Expires,
			"created":      sh.Created,
			"expired":      s.shares.Expired(sh),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSharesCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path     string `json:"path"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Expires  string `json:"expires"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
		return
	}
	if req.Name == "" {
		req.Name = filepath.Base(req.Path)
	}
	sh, err := s.shares.Create(req.Path, req.Name, req.Password, req.Expires)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":        sh.Token,
		"name":         sh.Name,
		"path":         sh.Path,
		"has_password": sh.PasswordHash != "",
		"expires":      sh.Expires,
		"created":      sh.Created,
	})
}

func (s *Server) handleSharesDelete(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нет токена"})
		return
	}
	if err := s.shares.Delete(token); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleUsersList(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	out := make([]map[string]string, 0, len(cfg.Users))
	for _, u := range cfg.Users {
		admin := "false"
		if u.Admin {
			admin = "true"
		}
		out = append(out, map[string]string{"username": u.Username, "admin": admin})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUsersCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Admin    bool   `json:"admin"`
	}
	if err := readJSON(r, &req); err != nil || req.Username == "" || len(req.Password) < 4 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "логин и пароль от 4 символов"})
		return
	}
	if _, exists := s.store.FindUser(req.Username); exists {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "пользователь уже есть"})
		return
	}
	hash, err := config.HashPassword(req.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	err = s.store.Update(func(c *config.Config) {
		c.Users = append(c.Users, config.User{Username: req.Username, PasswordHash: hash, Admin: req.Admin})
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleUsersDelete(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("username")
	sess := r.Context().Value(ctxSessionKey).(*auth.Session)
	if username == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нет пользователя"})
		return
	}
	if username == sess.Username {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нельзя удалить себя"})
		return
	}
	cfg := s.store.Get()
	admins := 0
	targetIsAdmin := false
	for _, u := range cfg.Users {
		if u.Admin {
			admins++
		}
		if u.Username == username && u.Admin {
			targetIsAdmin = true
		}
	}
	if targetIsAdmin && admins <= 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нельзя удалить последнего администратора"})
		return
	}
	err := s.store.Update(func(c *config.Config) {
		out := c.Users[:0]
		for _, u := range c.Users {
			if u.Username != username {
				out = append(out, u)
			}
		}
		c.Users = out
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	certNote := ""
	if cfg.HTTPS && cfg.TLSCert != "" {
		certNote, _ = certInfo(cfg.TLSCert)
	} else if cfg.HTTPS {
		certNote, _ = certInfo("data/cert.pem")
	}
	absRoot := cfg.RootDir
	if a, err := filepath.Abs(cfg.RootDir); err == nil {
		absRoot = a
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"web_port":            cfg.WebPort,
		"sftp_port":           cfg.SFTPPort,
		"bind":                cfg.Bind,
		"root_dir":            absRoot,
		"mounts":              cfg.Mounts,
		"https":               cfg.HTTPS,
		"zip_max_bytes":       cfg.ZipMaxBytes,
		"external_ip":         cfg.ExternalIP,
		"session_ttl_minutes": cfg.SessionTTLMinutes,
		"cert_info":           certNote,
	})
}

func (s *Server) handleMountsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Get().Mounts)
}

func (s *Server) handleMountsCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := readJSON(r, &req); err != nil || req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "укажите путь к папке"})
		return
	}
	abs, err := filepath.Abs(req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "папка не найдена"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = filepath.Base(abs)
	}
	if name == "" || name == "." || strings.ContainsAny(name, `/\`) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "недопустимое имя"})
		return
	}
	for _, m := range s.store.Get().Mounts {
		if strings.EqualFold(m.Name, name) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "папка с таким именем уже есть"})
			return
		}
	}

	mounts := append([]config.Mount{}, s.store.Get().Mounts...)
	if len(mounts) == 0 {
		m, err := vfs.LegacySeed(nil, s.store.Get().RootDir)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if strings.EqualFold(m.Name, name) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("имя %q занято корневой папкой — назовите иначе", name),
			})
			return
		}
		mounts = append(mounts, m)
	}
	mounts = append(mounts, config.Mount{Name: name, Path: abs})

	err = s.store.Update(func(c *config.Config) {
		c.Mounts = mounts
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "mounts": mounts})
}

func (s *Server) handleMountsDelete(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нет имени"})
		return
	}
	err := s.store.Update(func(c *config.Config) {
		out := c.Mounts[:0]
		for _, m := range c.Mounts {
			if !strings.EqualFold(m.Name, name) {
				out = append(out, m)
			}
		}
		c.Mounts = out
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	resp := map[string]interface{}{
		"drives":  vfs.Drives(),
		"entries": []entryJSON{},
	}
	if path == "" {
		resp["path"] = ""
		resp["parent"] = ""
		writeJSON(w, http.StatusOK, resp)
		return
	}
	abs, err := vfs.NormalizeBrowse(path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "не удалось открыть: " + err.Error()})
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
		if !info.IsDir() {
			continue
		}
		entries = append(entries, entryJSON{
			Name:    info.Name(),
			Path:    filepath.Join(abs, info.Name()),
			IsDir:   true,
			ModTime: info.ModTime().Format("02.01.2006 15:04"),
		})
	}
	resp["path"] = abs
	resp["parent"] = vfs.BrowseParent(abs)
	resp["entries"] = entries
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAuthedManifest(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	cfg := s.store.Get()
	abs, err := vfs.Resolve(cfg.Mounts, cfg.RootDir, rel)
	if err != nil || !fileExists(abs) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "не найдено"})
		return
	}
	withHash := true
	if hq := r.URL.Query().Get("hash"); hq == "0" || hq == "false" {
		withHash = false
	}
	name := filepath.Base(abs)
	m, err := s.shares.ManifestPath(abs, name, withHash)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleExe(w http.ResponseWriter, r *http.Request) {
	exe, err := os.Executable()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="fileshare.exe"`)
	http.ServeFile(w, r, exe)
}

func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WebPort           int    `json:"web_port"`
		SFTPPort          int    `json:"sftp_port"`
		Bind              string `json:"bind"`
		RootDir           string `json:"root_dir"`
		HTTPS             bool   `json:"https"`
		ZipMaxBytes       int64  `json:"zip_max_bytes"`
		ExternalIP        string `json:"external_ip"`
		SessionTTLMinutes int    `json:"session_ttl_minutes"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
		return
	}
	if req.WebPort < 1 || req.WebPort > 65535 || req.SFTPPort < 1 || req.SFTPPort > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "порты от 1 до 65535"})
		return
	}
	if req.Bind == "" {
		req.Bind = "0.0.0.0"
	}
	if req.RootDir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "укажите корень файлов"})
		return
	}
	absRoot, err := filepath.Abs(req.RootDir)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не удалось создать корень: " + err.Error()})
		return
	}
	if req.ZipMaxBytes < 1 {
		req.ZipMaxBytes = 1
	}
	if req.SessionTTLMinutes < 1 {
		req.SessionTTLMinutes = 1
	}

	old := s.store.Get()
	if req.HTTPS {
		if _, _, err := ensureCert(old.TLSCert, old.TLSKey, req.ExternalIP); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "сертификат: " + err.Error()})
			return
		}
	}

	revertConfig := func() {
		s.store.Update(func(c *config.Config) {
			c.WebPort = old.WebPort
			c.SFTPPort = old.SFTPPort
			c.Bind = old.Bind
			c.RootDir = old.RootDir
			c.HTTPS = old.HTTPS
			c.ZipMaxBytes = old.ZipMaxBytes
			c.ExternalIP = old.ExternalIP
			c.SessionTTLMinutes = old.SessionTTLMinutes
		})
	}

	err = s.store.Update(func(c *config.Config) {
		c.WebPort = req.WebPort
		c.SFTPPort = req.SFTPPort
		c.Bind = req.Bind
		c.RootDir = absRoot
		c.HTTPS = req.HTTPS
		c.ZipMaxBytes = req.ZipMaxBytes
		c.ExternalIP = req.ExternalIP
		c.SessionTTLMinutes = req.SessionTTLMinutes
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if err := s.sftp.Start(req.Bind, req.SFTPPort); err != nil {
		revertConfig()
		if err2 := s.sftp.Start(old.Bind, old.SFTPPort); err2 != nil {
			s.logger.Printf("SFTP откат не удался: %v", err2)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "SFTP порт: " + err.Error()})
		return
	}
	if err := s.Start(req.Bind, req.WebPort, req.HTTPS); err != nil {
		revertConfig()
		if err2 := s.sftp.Start(old.Bind, old.SFTPPort); err2 != nil {
			s.logger.Printf("SFTP откат не удался: %v", err2)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "веб порт: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) publicBase() string {
	cfg := s.store.Get()
	scheme := schemeStr(cfg.HTTPS)
	ifaces, _ := netInterfaces()
	host := strings.TrimSpace(cfg.ExternalIP)
	if host == "" {
		host = s.fetchExternalIP()
	}
	if host == "" {
		host = maybeLAN(ifaces)
	}
	if host == "" {
		return ""
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	port := strconvItoa(cfg.WebPort)
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		return scheme + "://" + host
	}
	return scheme + "://" + host + ":" + port
}

func (s *Server) handlePublicBase(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"public_base": s.publicBase()})
}

func (s *Server) handleNetInfo(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	var locals []string
	ifaces, _ := netInterfaces()
	locals = append(locals, ifaces...)
	ext := cfg.ExternalIP
	if ext == "" {
		ext = s.fetchExternalIP()
	}
	lan := firstNonEmpty(maybeLAN(locals), "ваш LAN-IP")
	instructions := fmt.Sprintf(
		"1. В настройках роутера найдите раздел «Port Forwarding» / «Проброс портов» / «NAT».\n"+
			"2. Добавьте правила:\n"+
			"   • внешний порт %d → %s:%d (TCP) — веб-интерфейс\n"+
			"   • внешний порт %d → %s:%d (TCP) — SFTP\n"+
			"3. Веб: %s   SFTP: %s:%d\n"+
			"4. Разрешите эти порты в брандмауэре Windows (входящие подключения).",
		cfg.WebPort, lan, cfg.WebPort, cfg.SFTPPort, lan, cfg.SFTPPort,
		schemeStr(cfg.HTTPS)+"://"+firstNonEmpty(ext, "ВАШ_ИП")+":"+strconvItoa(cfg.WebPort),
		firstNonEmpty(ext, "ВАШ_ИП"), cfg.SFTPPort,
	)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"local_ips":    locals,
		"external_ip":  ext,
		"public_base":  s.publicBase(),
		"web_port":     cfg.WebPort,
		"sftp_port":    cfg.SFTPPort,
		"lan_ip":       lan,
		"instructions": instructions,
		"https":        cfg.HTTPS,
	})
}

func schemeStr(https bool) string {
	if https {
		return "https"
	}
	return "http"
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func strconvItoa(n int) string {
	return fmt.Sprintf("%d", n)
}

func maybeLAN(ips []string) string {
	for _, ip := range ips {
		if strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.") {
			return ip
		}
	}
	if len(ips) > 0 {
		return ips[0]
	}
	return ""
}
