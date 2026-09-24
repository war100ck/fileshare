package ftpserver

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"fileshare/internal/config"
	"fileshare/internal/vfs"
)

type Server struct {
	store  *config.Store
	logger *log.Logger

	mu sync.Mutex
	ln net.Listener
}

func New(store *config.Store, logger *log.Logger) *Server {
	return &Server{store: store, logger: logger}
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
	s.logger.Printf("FTP слушает %s", addr)
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
			s.logger.Printf("PANIC ftp conn: %v\n%s", r, debug.Stack())
		}
		nConn.Close()
	}()
	if tc, ok := nConn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(30 * time.Second)
	}
	ss := newSession(s, nConn)
	ss.loop()
}

type session struct {
	srv  *Server
	conn net.Conn
	br   *bufio.Reader

	cfg       config.Config
	legacyAbs string

	user     string
	pass     string
	logged   bool
	cwd      string
	typ      byte
	rest     int64
	rnfr     string
	pasvLn   *net.TCPListener
	portAddr string
}

func newSession(srv *Server, conn net.Conn) *session {
	cfg := srv.store.Get()
	legacy, err := filepath.Abs(cfg.RootDir)
	if err != nil {
		legacy = cfg.RootDir
	}
	return &session{
		srv:       srv,
		conn:      conn,
		br:        bufio.NewReaderSize(conn, 8192),
		cfg:       cfg,
		legacyAbs: legacy,
		cwd:       "/",
		typ:       'A',
	}
}

func (ss *session) reply(code int, msg string) {
	fmt.Fprintf(ss.conn, "%d %s\r\n", code, msg)
}

func (ss *session) replyf(code int, format string, args ...interface{}) {
	ss.reply(code, fmt.Sprintf(format, args...))
}

func (ss *session) closePasv() {
	if ss.pasvLn != nil {
		ss.pasvLn.Close()
		ss.pasvLn = nil
	}
}

func (ss *session) loop() {
	defer ss.closePasv()
	_ = ss.conn.SetReadDeadline(time.Now().Add(30 * time.Minute))
	ss.reply(220, "fileshare FTP server ready")
	for {
		line, err := ss.br.ReadString('\n')
		if err != nil {
			return
		}
		_ = ss.conn.SetReadDeadline(time.Now().Add(30 * time.Minute))
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		if err := ss.dispatch(line); err != nil {
			return
		}
	}
}

func (ss *session) dispatch(line string) error {
	verb, arg := line, ""
	if i := strings.IndexByte(line, ' '); i >= 0 {
		verb, arg = line[:i], strings.TrimLeft(line[i+1:], " ")
	}
	verb = strings.ToUpper(verb)

	if verb != "USER" && verb != "PASS" && verb != "QUIT" && verb != "SYST" &&
		verb != "FEAT" && verb != "NOOP" && verb != "AUTH" && verb != "OPTS" &&
		verb != "PBSZ" && verb != "PROT" && verb != "HELP" && verb != "STAT" {
		if !ss.logged {
			ss.reply(530, "Please log in with USER and PASS.")
			return nil
		}
	}

	switch verb {
	case "USER":
		ss.user = arg
		ss.logged = false
		ss.reply(331, "User name okay, need password.")
	case "PASS":
		if ss.user == "" {
			ss.reply(503, "Login with USER first.")
			return nil
		}
		if _, ok := ss.srv.store.CheckPassword(ss.user, arg); ok {
			ss.logged = true
			ss.reply(230, "User logged in, proceed.")
		} else {
			ss.reply(530, "Login incorrect.")
		}
		_ = arg
		ss.pass = ""
	case "QUIT":
		ss.reply(221, "Goodbye.")
		return fmt.Errorf("quit")
	case "SYST":
		ss.reply(215, "UNIX Type: L8")
	case "FEAT":
		fmt.Fprint(ss.conn, "211-Features:\r\n SIZE\r\n MDTM\r\n REST STREAM\r\n UTF8\r\n TVFS\r\n EPSV\r\n PASV\r\n211 End\r\n")
	case "OPTS":
		ss.reply(200, "OPTS command ok.")
	case "PBSZ":
		ss.reply(200, "PBSZ=0")
	case "PROT":
		ss.reply(536, "Protection level not supported (plain FTP only).")
	case "AUTH":
		ss.reply(534, "Unusable AUTH, try TLS first. (шифрование FTP не поддерживается — отключите «Шифровать»)")
	case "NOOP":
		ss.reply(200, "NOOP ok.")
	case "HELP":
		ss.reply(214, "USER PASS RETR STOR LIST NLST CWD PWD SIZE MDTM MKD DELE RMD RNFR RNTO PASV EPSV PORT REST QUIT")
	case "STAT":
		if arg == "" {
			ss.reply(211, "fileshare FTP server status: logged in")
		} else {
			ss.reply(211, "End of status")
		}
	case "PWD", "XPWD":
		quoted := strings.ReplaceAll(ss.cwd, `"`, `""`)
		ss.replyf(257, `"%s" is current directory.`, quoted)
	case "CWD", "XCWD":
		vp := ss.virtPath(arg)
		if err := ss.checkCWD(vp); err != nil {
			ss.reply(550, "Failed to change directory.")
		} else {
			ss.cwd = vp
			ss.reply(250, "Directory changed.")
		}
	case "CDUP", "XCUP":
		vp := path.Dir(ss.cwd)
		if vp == "." || vp == "" {
			vp = "/"
		}
		if err := ss.checkCWD(vp); err != nil {
			ss.reply(550, "Failed to change directory.")
		} else {
			ss.cwd = vp
			ss.reply(250, "Directory changed.")
		}
	case "TYPE":
		t := strings.ToUpper(strings.TrimSpace(arg))
		if t == "A" || t == "I" || t == "L 8" || t == "L8" {
			ss.typ = t[0]
			ss.reply(200, "Type set to "+t)
		} else {
			ss.reply(504, "Unsupported TYPE.")
		}
	case "MODE":
		if strings.EqualFold(strings.TrimSpace(arg), "S") {
			ss.reply(200, "Mode set to S.")
		} else {
			ss.reply(504, "Unsupported MODE.")
		}
	case "STRU":
		if strings.EqualFold(strings.TrimSpace(arg), "F") {
			ss.reply(200, "Structure set to F.")
		} else {
			ss.reply(504, "Unsupported STRU.")
		}
	case "PASV":
		ss.closePasv()
		ln, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			ss.reply(425, "Cannot open passive connection.")
			return nil
		}
		tcpLn := ln.(*net.TCPListener)
		ss.pasvLn = tcpLn
		port := tcpLn.Addr().(*net.TCPAddr).Port
		ip := ss.localIP()
		p1, p2 := port/256, port%256
		ss.replyf(227, "Entering Passive Mode (%d,%d,%d,%d,%d,%d).", ip[0], ip[1], ip[2], ip[3], p1, p2)
	case "EPSV":
		ss.closePasv()
		ln, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			ss.reply(425, "Cannot open passive connection.")
			return nil
		}
		ss.pasvLn = ln.(*net.TCPListener)
		port := ss.pasvLn.Addr().(*net.TCPAddr).Port
		ss.replyf(229, "Entering Extended Passive Mode (|||%d|).", port)
	case "PORT":
		parts := strings.Split(strings.TrimSpace(arg), ",")
		if len(parts) != 6 {
			ss.reply(501, "Bad PORT command.")
			return nil
		}
		var b [6]int
		for i, p := range parts {
			v, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil || v < 0 || v > 255 {
				ss.reply(501, "Bad PORT command.")
				return nil
			}
			b[i] = v
		}
		ss.closePasv()
		ss.portAddr = fmt.Sprintf("%d.%d.%d.%d:%d", b[0], b[1], b[2], b[3], b[4]*256+b[5])
		ss.reply(200, "PORT command successful.")
	case "EPRT":
		parts := strings.Split(strings.TrimSpace(arg), "|")
		if len(parts) != 4 || parts[1] != "1" {
			ss.reply(522, "Network protocol not supported, use (1,2).")
			return nil
		}
		port, err := strconv.Atoi(parts[3])
		if err != nil || port <= 0 || port > 65535 {
			ss.reply(501, "Bad EPRT command.")
			return nil
		}
		ss.closePasv()
		ss.portAddr = net.JoinHostPort(parts[2], strconv.Itoa(port))
		ss.reply(200, "EPRT command successful.")
	case "LIST":
		ss.cmdList(arg, false)
	case "NLST":
		ss.cmdList(arg, true)
	case "RETR":
		ss.cmdRetr(arg)
	case "STOR":
		ss.cmdStor(arg, false)
	case "APPE":
		ss.cmdStor(arg, true)
	case "SIZE":
		vp := ss.virtPath(arg)
		if vp == "/" {
			ss.reply(550, "Not a regular file.")
			return nil
		}
		p, _, err := ss.resolve(vp)
		if err != nil {
			ss.reply(550, "File not found.")
			return nil
		}
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			ss.reply(550, "Not a regular file.")
			return nil
		}
		ss.replyf(213, "%d", fi.Size())
	case "MDTM":
		vp := ss.virtPath(arg)
		var mt time.Time
		if vp == "/" {
			ss.reply(550, "Not a regular file.")
			return nil
		}
		p, _, err := ss.resolve(vp)
		if err == nil {
			if fi, err2 := os.Stat(p); err2 == nil {
				mt = fi.ModTime()
			} else {
				err = err2
			}
		}
		if err != nil {
			ss.reply(550, "File not found.")
			return nil
		}
		ss.replyf(213, "%s", mt.Format("20060102150405"))
	case "REST":
		v, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
		if err != nil || v < 0 {
			ss.reply(501, "Bad REST offset.")
			return nil
		}
		ss.rest = v
		ss.reply(350, "Restarting at offset.")
	case "MKD", "XMKD":
		vp := ss.virtPath(arg)
		if vp == "/" {
			ss.reply(550, "Cannot create root directory.")
			return nil
		}
		p, _, err := ss.resolve(vp)
		if err != nil {
			ss.reply(550, "Cannot create directory.")
			return nil
		}
		if err := os.Mkdir(p, 0o755); err != nil {
			ss.reply(550, "Cannot create directory: "+errMessage(err))
			return nil
		}
		ss.replyf(257, `"%s" created.`, strings.ReplaceAll(vp, `"`, `""`))
	case "RMD", "XRMD":
		vp := ss.virtPath(arg)
		if vp == "/" {
			ss.reply(550, "Cannot remove root directory.")
			return nil
		}
		p, _, err := ss.resolve(vp)
		if err != nil {
			ss.reply(550, "Cannot remove directory.")
			return nil
		}
		if err := os.Remove(p); err != nil {
			ss.reply(550, "Cannot remove directory: "+errMessage(err))
			return nil
		}
		ss.reply(250, "Directory removed.")
	case "DELE":
		vp := ss.virtPath(arg)
		if vp == "/" {
			ss.reply(550, "Cannot delete root.")
			return nil
		}
		p, _, err := ss.resolve(vp)
		if err != nil {
			ss.reply(550, "Cannot delete file.")
			return nil
		}
		if err := os.Remove(p); err != nil {
			ss.reply(550, "Cannot delete file: "+errMessage(err))
			return nil
		}
		ss.reply(250, "File deleted.")
	case "RNFR":
		vp := ss.virtPath(arg)
		if vp == "/" {
			ss.reply(550, "Cannot rename root.")
			return nil
		}
		p, _, err := ss.resolve(vp)
		if err != nil {
			ss.reply(550, "File not found.")
			return nil
		}
		if _, err := os.Stat(p); err != nil {
			ss.reply(550, "File not found.")
			return nil
		}
		ss.rnfr = p
		ss.reply(350, "Ready for RNTO.")
	case "RNTO":
		if ss.rnfr == "" {
			ss.reply(503, "RNFR required first.")
			return nil
		}
		vp := ss.virtPath(arg)
		p, _, err := ss.resolve(vp)
		if err != nil {
			ss.reply(550, "Rename failed.")
			ss.rnfr = ""
			return nil
		}
		err = os.Rename(ss.rnfr, p)
		ss.rnfr = ""
		if err != nil {
			ss.reply(550, "Rename failed: "+errMessage(err))
			return nil
		}
		ss.reply(250, "Rename successful.")
	case "ABOR":
		ss.reply(226, "Aborted.")
	default:
		ss.replyf(502, "Command %q not implemented.", verb)
	}
	return nil
}

func (ss *session) localIP() net.IP {
	if ta, ok := ss.conn.LocalAddr().(*net.TCPAddr); ok && ta.IP != nil {
		if v4 := ta.IP.To4(); v4 != nil {
			return v4
		}
	}
	return net.IPv4(127, 0, 0, 1)
}

func (ss *session) virtPath(arg string) string {
	arg = strings.TrimSpace(arg)
	if strings.HasPrefix(arg, "/") {
		return path.Clean(arg)
	}
	return path.Clean(path.Join(ss.cwd, arg))
}

func (ss *session) resolve(vp string) (string, bool, error) {
	if vp == "/" {
		return "", true, nil
	}
	abs, err := vfs.Resolve(ss.cfg.Mounts, ss.legacyAbs, vp)
	if err != nil {
		return "", false, err
	}
	return abs, false, nil
}

func (ss *session) checkCWD(vp string) error {
	if vp == "/" {
		if len(ss.cfg.Mounts) == 0 {
			if fi, err := os.Stat(ss.legacyAbs); err != nil || !fi.IsDir() {
				return os.ErrNotExist
			}
		}
		return nil
	}
	p, _, err := ss.resolve(vp)
	if err != nil {
		return err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return os.ErrExist
	}
	return nil
}

func (ss *session) rootEntries() []os.FileInfo {
	var out []os.FileInfo
	for _, m := range ss.cfg.Mounts {
		fi, err := os.Stat(m.Path)
		if err != nil {
			continue
		}
		out = append(out, namedInfo{FileInfo: fi, name: m.Name})
	}
	return out
}

type namedInfo struct {
	os.FileInfo
	name string
}

func (n namedInfo) Name() string { return n.name }

func (ss *session) listEntries(vp string) ([]os.FileInfo, error) {
	if vp == "/" && len(ss.cfg.Mounts) > 0 {
		return ss.rootEntries(), nil
	}
	p, isRoot, err := ss.resolve(vp)
	if err != nil {
		return nil, err
	}
	if isRoot {
		p = ss.legacyAbs
	}
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return []os.FileInfo{fi}, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdir(-1)
}

func (ss *session) cmdList(arg string, namesOnly bool) {
	vp := ss.virtPath(arg)
	entries, err := ss.listEntries(vp)
	if err != nil {
		ss.reply(550, "Failed to open directory.")
		return
	}
	ss.reply(150, "Here comes the directory listing.")
	data, err := ss.openData()
	if err != nil {
		ss.reply(425, "Use PASV or PORT first.")
		return
	}
	defer data.Close()
	w := bufio.NewWriter(data)
	base := vp
	if base == "/" {
		base = ""
	}
	for _, fi := range entries {
		name := fi.Name()
		if namesOnly {
			fmt.Fprintf(w, "%s\r\n", name)
		} else {
			fmt.Fprintf(w, "%s\r\n", lsLine(ss.user, name, fi))
		}
	}
	_ = w.Flush()
	ss.reply(226, "Directory send OK.")
	_ = base
}

func (ss *session) cmdRetr(arg string) {
	vp := ss.virtPath(arg)
	if vp == "/" {
		ss.reply(550, "Not a regular file.")
		return
	}
	p, _, err := ss.resolve(vp)
	if err != nil {
		ss.reply(550, "File not found.")
		return
	}
	f, err := os.Open(p)
	if err != nil {
		ss.reply(550, "File not found.")
		return
	}
	defer f.Close()
	offset := ss.rest
	ss.rest = 0
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			ss.reply(550, "Seek failed.")
			return
		}
	}
	mode := "ASCII"
	if ss.typ == 'I' {
		mode = "binary"
	}
	ss.replyf(150, "Opening %s mode data connection for %s.", mode, path.Base(vp))
	data, err := ss.openData()
	if err != nil {
		ss.reply(425, "Use PASV or PORT first.")
		return
	}
	defer data.Close()
	if _, err := io.Copy(data, f); err != nil {
		ss.reply(426, "Transfer aborted.")
		return
	}
	ss.reply(226, "Transfer complete.")
}

func (ss *session) cmdStor(arg string, appendMode bool) {
	vp := ss.virtPath(arg)
	if vp == "/" {
		ss.reply(550, "Cannot write to root.")
		return
	}
	p, _, err := ss.resolve(vp)
	if err != nil {
		ss.reply(550, "Cannot create file.")
		return
	}
	flags := os.O_WRONLY | os.O_CREATE
	if appendMode {
		flags |= os.O_APPEND
	} else if ss.rest == 0 {
		flags |= os.O_TRUNC
	}
	offset := ss.rest
	ss.rest = 0
	f, err := os.OpenFile(p, flags, 0o644)
	if err != nil {
		ss.reply(550, "Cannot create file: "+errMessage(err))
		return
	}
	defer f.Close()
	if !appendMode && offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			ss.reply(550, "Seek failed.")
			return
		}
	}
	mode := "ASCII"
	if ss.typ == 'I' {
		mode = "binary"
	}
	ss.replyf(150, "Opening %s mode data connection for %s.", mode, path.Base(vp))
	data, err := ss.openData()
	if err != nil {
		ss.reply(425, "Use PASV or PORT first.")
		return
	}
	defer data.Close()
	if _, err := io.Copy(f, data); err != nil {
		ss.reply(426, "Transfer aborted.")
		return
	}
	ss.reply(226, "Transfer complete.")
}

func (ss *session) openData() (net.Conn, error) {
	if ss.pasvLn != nil {
		ln := ss.pasvLn
		ss.pasvLn = nil
		defer ln.Close()
		_ = ln.SetDeadline(time.Now().Add(30 * time.Second))
		conn, err := ln.Accept()
		if err != nil {
			return nil, err
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetKeepAlive(true)
			_ = tc.SetKeepAlivePeriod(30 * time.Second)
		}
		return conn, nil
	}
	if ss.portAddr != "" {
		conn, err := net.DialTimeout("tcp", ss.portAddr, 10*time.Second)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
	return nil, fmt.Errorf("no data connection mode")
}

func lsLine(owner, name string, fi os.FileInfo) string {
	if owner == "" {
		owner = "ftp"
	}
	if len(owner) > 8 {
		owner = owner[:8]
	}
	perms := "-rw-rw-rw-"
	switch {
	case fi.IsDir():
		perms = "drwxrwxrwx"
	case fi.Mode()&os.ModeSymlink != 0:
		perms = "lrwxrwxrwx"
	}
	links := 1
	if fi.IsDir() {
		links = 2
	}
	group := "ftp"
	size := fi.Size()
	if fi.IsDir() {
		size = 0
	}
	t := fi.ModTime()
	now := time.Now()
	half := 180 * 24 * time.Hour
	var date string
	if now.Sub(t) > half || t.Sub(now) > half {
		date = t.Format("Jan _2  2006")
	} else {
		date = t.Format("Jan _2 15:04")
	}
	return fmt.Sprintf("%s %3d %-8s %-8s %13d %s %s", perms, links, owner, group, size, date, name)
}

func errMessage(err error) string {
	if os.IsNotExist(err) {
		return "not found"
	}
	if os.IsPermission(err) {
		return "access denied"
	}
	msg := err.Error()
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return msg
}
