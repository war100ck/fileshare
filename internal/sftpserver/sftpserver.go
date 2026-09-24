package sftpserver

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"fileshare/internal/config"
	"fileshare/internal/vfs"
)

type Server struct {
	store  *config.Store
	logger *log.Logger

	mu     sync.Mutex
	ln     net.Listener
	sshCfg *ssh.ServerConfig
}

func New(store *config.Store, dataDir string, logger *log.Logger) (*Server, error) {
	hostKey, err := config.LoadOrGenerateHostKey(dataDir)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		return nil, fmt.Errorf("host signer: %w", err)
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if _, ok := store.CheckPassword(c.User(), string(pass)); ok {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("неверный логин или пароль")
		},
		ServerVersion: "SSH-2.0-fileshare",
	}
	cfg.AddHostKey(signer)

	s := &Server{store: store, logger: logger, sshCfg: cfg}
	return s, nil
}

func (s *Server) Start(bind string, port int) error {
	s.mu.Lock()
	old := s.ln
	s.ln = nil
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	addr := fmt.Sprintf("%s:%d", bind, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	s.logger.Printf("SFTP слушает %s", addr)
	go s.acceptLoop(ln)
	return nil
}

func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		err := s.ln.Close()
		s.ln = nil
		return err
	}
	return nil
}

func (s *Server) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(nConn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Printf("PANIC sftp conn: %v\n%s", r, debug.Stack())
		}
		nConn.Close()
	}()
	if tc, ok := nConn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(30 * time.Second)
	}
	sshConn, chans, reqs, err := ssh.NewServerConn(nConn, s.sshCfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)

	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if _, _, err := sshConn.SendRequest("keepalive@openssh.com", false, nil); err != nil {
					return
				}
			}
		}
	}()

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, requests, err := newChan.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					s.logger.Printf("PANIC sftp session %s: %v\n%s", sshConn.User(), r, debug.Stack())
				}
			}()
			s.handleSession(ch, requests, sshConn.User())
		}()
	}
}

func (s *Server) handleSession(ch ssh.Channel, requests <-chan *ssh.Request, user string) {
	defer ch.Close()
	cfg := s.store.Get()
	root := cfg.RootDir
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}
	handlers := newJailHandlers(absRoot, cfg.Mounts)

	for req := range requests {
		switch req.Type {
		case "subsystem":
			var payload struct {
				Name string
			}
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil || payload.Name != "sftp" {
				if req.WantReply {
					req.Reply(false, nil)
				}
				continue
			}
			if req.WantReply {
				req.Reply(true, nil)
			}
			rs := sftp.NewRequestServer(ch, handlers)
			err := rs.Serve()
			if err != nil && err != io.EOF {
				s.logger.Printf("sftp %s: %v", user, err)
			}
			rs.Close()
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			return
		case "shell", "exec":
			var ep struct {
				Command string
			}
			if req.Type == "exec" {
				_ = ssh.Unmarshal(req.Payload, &ep)
			}
			if req.WantReply {
				req.Reply(true, nil)
			}
			cmd := strings.ToLower(ep.Command)
			if req.Type == "shell" || strings.Contains(cmd, "locale") ||
				strings.Contains(cmd, "lang") || strings.Contains(cmd, "lc_") {
				ch.Write([]byte("LANG=en_US.UTF-8\nLC_ALL=en_US.UTF-8\nLC_CTYPE=en_US.UTF-8\nLANGUAGE=en_US.UTF-8\n"))
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			}
			ch.Write([]byte("fileshare: доступен только SFTP\r\n"))
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
			return
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

type fsRoot struct {
	legacy string
	mounts []config.Mount
}

func (f *fsRoot) resolve(p string) (string, error) {
	return vfs.Resolve(f.mounts, f.legacy, p)
}

type jailHandlers struct {
	root *fsRoot
}

func newJailHandlers(root string, mounts []config.Mount) sftp.Handlers {
	h := &jailHandlers{root: &fsRoot{legacy: filepath.Clean(root), mounts: mounts}}
	return sftp.Handlers{
		FileGet:  h,
		FilePut:  h,
		FileCmd:  h,
		FileList: h,
	}
}

func (h *jailHandlers) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	p, err := h.root.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (h *jailHandlers) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	p, err := h.root.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}
	pf := r.Pflags()
	flags := 0
	switch {
	case pf.Read && pf.Write:
		flags |= os.O_RDWR
	case pf.Write:
		flags |= os.O_WRONLY
	default:
		flags |= os.O_RDONLY
	}
	if pf.Creat {
		flags |= os.O_CREATE
	}
	if pf.Trunc {
		flags |= os.O_TRUNC
	}
	if pf.Excl {
		flags |= os.O_EXCL
	}
	return os.OpenFile(p, flags, 0o644)
}

func (h *jailHandlers) Filecmd(r *sftp.Request) error {
	switch r.Method {
	case "Setstat":
		p, err := h.root.resolve(r.Filepath)
		if err != nil {
			return err
		}
		af := r.AttrFlags()
		st := r.Attributes()
		if st == nil {
			return nil
		}
		if af.Permissions {
			if err := os.Chmod(p, st.FileMode()); err != nil {
				return err
			}
		}
		if af.Size {
			if err := os.Truncate(p, int64(st.Size)); err != nil {
				return err
			}
		}
		if af.Acmodtime {
			at := st.AccessTime()
			mt := st.ModTime()
			_ = os.Chtimes(p, at, mt)
		}
		return nil
	case "Rename":
		src, err := h.root.resolve(r.Filepath)
		if err != nil {
			return err
		}
		dst, err := h.root.resolve(r.Target)
		if err != nil {
			return err
		}
		return os.Rename(src, dst)
	case "Mkdir":
		p, err := h.root.resolve(r.Filepath)
		if err != nil {
			return err
		}
		return os.Mkdir(p, 0o755)
	case "Rmdir":
		p, err := h.root.resolve(r.Filepath)
		if err != nil {
			return err
		}
		return os.RemoveAll(p)
	case "Remove":
		p, err := h.root.resolve(r.Filepath)
		if err != nil {
			return err
		}
		return os.Remove(p)
	case "Symlink", "Link":
		return fmt.Errorf("символические ссылки отключены")
	default:
		return fmt.Errorf("неподдерживаемая операция: %s", r.Method)
	}
}

type listerAt []os.FileInfo

func (l listerAt) ListAt(dst []os.FileInfo, off int64) (int, error) {
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(dst, l[off:])
	if off+int64(n) >= int64(len(l)) {
		return n, io.EOF
	}
	return n, nil
}

type namedInfo struct {
	os.FileInfo
	name string
}

func (n namedInfo) Name() string { return n.name }

func (f *fsRoot) isRootPath(p string) bool {
	return vfs.CleanVirtual(p) == ""
}

func (f *fsRoot) rootEntries() listerAt {
	var out listerAt
	for _, m := range f.mounts {
		fi, err := os.Stat(m.Path)
		if err != nil {
			continue
		}
		out = append(out, namedInfo{FileInfo: fi, name: m.Name})
	}
	return out
}

func (h *jailHandlers) Readlink(p string) (string, error) {
	abs, err := h.root.resolve(p)
	if err != nil {
		return "", err
	}
	return os.Readlink(abs)
}

func (h *jailHandlers) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	switch r.Method {
	case "List":
		if len(h.root.mounts) > 0 && h.root.isRootPath(r.Filepath) {
			return h.root.rootEntries(), nil
		}
		p, err := h.root.resolve(r.Filepath)
		if err != nil {
			return nil, err
		}
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		entries, err := f.Readdir(-1)
		if err != nil {
			return nil, err
		}
		return listerAt(entries), nil
	case "Stat":
		if len(h.root.mounts) > 0 && h.root.isRootPath(r.Filepath) {
			entries := h.root.rootEntries()
			if len(entries) > 0 {
				return listerAt{namedInfo{FileInfo: entries[0], name: "."}}, nil
			}
			return nil, os.ErrNotExist
		}
		p, err := h.root.resolve(r.Filepath)
		if err != nil {
			return nil, err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		return listerAt{fi}, nil
	case "Readlink":
		p, err := h.root.resolve(r.Filepath)
		if err != nil {
			return nil, err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		return listerAt{fi}, nil
	default:
		return nil, fmt.Errorf("неподдерживаемый list: %s", r.Method)
	}
}
