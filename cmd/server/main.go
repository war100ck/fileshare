package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"fileshare/internal/auth"
	"fileshare/internal/config"
	"fileshare/internal/downloader"
	"fileshare/internal/ftpserver"
	"fileshare/internal/sftpserver"
	"fileshare/internal/shares"
	"fileshare/internal/web"
)

func fatal(logger *log.Logger, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	logger.Print(msg)
	fmt.Println()
	fmt.Println("!!! " + msg)
	pauseIfConsole()
	os.Exit(1)
}

func pauseIfConsole() {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return
	}
	fmt.Print("Нажмите Enter для выхода...")
	fmt.Scanln()
}

func portBusy(err error) bool {
	s := err.Error()
	return strings.Contains(s, "Only one usage") ||
		strings.Contains(s, "address already in use") ||
		strings.Contains(s, "Only one usage of each socket address")
}

func openBrowser(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		c = exec.Command("cmd", "/c", "start", "", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	_ = c.Start()
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "download" {
		runDownload(os.Args[2:])
		return
	}

	fs := flag.NewFlagSet("fileshare", flag.ExitOnError)
	cfgPath := fs.String("config", "config.json", "путь к config.json")
	noBrowser := fs.Bool("nobrowser", false, "не открывать браузер при запуске")
	fs.Parse(os.Args[1:])

	logger := log.New(os.Stdout, "", log.LstdFlags)

	if err := config.EnsureDir("data"); err != nil {
		fatal(logger, "создать папку data: %v", err)
	}
	if lf, err := os.OpenFile("data/server.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		logger = log.New(io.MultiWriter(os.Stdout, lf), "", log.LstdFlags)
	} else {
		logger.Printf("лог-файл недоступен: %v", err)
	}
	store, err := config.Open(*cfgPath)
	if err != nil {
		fatal(logger, "конфиг: %v", err)
	}
	cfg := store.Get()
	if err := config.EnsureDir(cfg.RootDir); err != nil {
		fatal(logger, "корень файлов: %v", err)
	}

	authMgr := auth.NewManager(time.Duration(cfg.SessionTTLMinutes) * time.Minute)
	shareStore, err := shares.New(store, "data")
	if err != nil {
		fatal(logger, "shares: %v", err)
	}
	sftpSrv, err := sftpserver.New(store, "data", logger)
	if err != nil {
		fatal(logger, "sftp: %v", err)
	}
	ftpSrv := ftpserver.New(store, logger)
	webSrv := web.New(store, authMgr, shareStore, sftpSrv, logger)

	if err := sftpSrv.Start(cfg.Bind, cfg.SFTPPort); err != nil {
		if portBusy(err) {
			fatal(logger, "Порт SFTP %d уже занят — вероятно, fileshare уже запущен в другом окне. Закройте старое окно и повторите.", cfg.SFTPPort)
		}
		fatal(logger, "SFTP запуск: %v", err)
	}
	if err := ftpSrv.Start(cfg.Bind, cfg.FTPPort); err != nil {
		logger.Printf("FTP на порту %d не запустился: %v (веб и SFTP продолжают работать; другой порт задаётся полем ftp_port в config.json)", cfg.FTPPort, err)
	}
	if err := webSrv.Start(cfg.Bind, cfg.WebPort, cfg.HTTPS); err != nil {
		if portBusy(err) {
			fatal(logger, "Порт %d уже занят — вероятно, fileshare уже запущен в другом окне. Закройте старое окно и повторите.", cfg.WebPort)
		}
		fatal(logger, "веб запуск: %v", err)
	}
	go webSrv.WarmExternalIP()

	first := store.Get()
	if len(first.Users) == 1 && first.Users[0].Username == "admin" {
		logger.Printf("ВНИМАНИЕ: логин admin, пароль admin — смените пароль в веб-интерфейсе!")
	}
	logger.Printf("Сервер работает. Не закрывайте это окно (Ctrl+C — остановка).")

	if !*noBrowser {
		time.Sleep(500 * time.Millisecond)
		openBrowser(webSrv.BaseURL())
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	logger.Printf("остановка...")
	webSrv.Stop()
	sftpSrv.Stop()
	ftpSrv.Stop()
}

func runDownload(args []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && (len(args) == 1 || strings.HasPrefix(args[1], "-")) {
		url := args[0]
		rest := append([]string{}, args[1:]...)
		args = append(rest, url)
	}
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	out := fs.String("o", ".", "папка назначения")
	jobs := fs.Int("j", 8, "параллельных файлов")
	password := fs.String("password", "", "пароль публичной ссылки")
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Println("Использование: fileshare download <ссылка> [-o папка] [-j N] [-password пароль]")
		fmt.Println("Пример:       fileshare download http://192.168.1.5:8080/s/токен -o Games")
		os.Exit(2)
	}
	err := downloader.Run(downloader.Options{
		URL:      fs.Arg(0),
		Out:      *out,
		Jobs:     *jobs,
		Password: *password,
	})
	if err != nil {
		fmt.Println("Ошибка:", strings.TrimSpace(err.Error()))
		os.Exit(1)
	}
}
