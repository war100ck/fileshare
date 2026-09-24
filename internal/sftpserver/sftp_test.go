package sftpserver

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"fileshare/internal/config"
)

func TestSFTPJail(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "shared")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tmp, "config.json")
	store, err := config.Open(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(c *config.Config) { c.RootDir = root }); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	srv, err := New(store, filepath.Join(tmp, "data"), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start("127.0.0.1", port); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	conn, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port),
		&ssh.ClientConfig{
			User:            "admin",
			Auth:            []ssh.AuthMethod{ssh.Password("admin")},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		})
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	defer conn.Close()

	sftpClient, err := sftp.NewClient(conn)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	defer sftpClient.Close()

	payload := []byte("hello fileshare")
	if err := sftpClient.Mkdir("/sub"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := sftpClient.Create("/sub/a.txt")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()

	rf, err := sftpClient.Open("/sub/a.txt")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := io.ReadAll(rf)
	rf.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("content mismatch: %q", got)
	}

	entries, err := sftpClient.ReadDir("/")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "sub" {
		t.Fatalf("unexpected dir listing: %v", entries)
	}

	onDisk := filepath.Join(root, "sub", "a.txt")
	data, err := os.ReadFile(onDisk)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("file not on disk in root: %v", err)
	}

	if _, err := sftpClient.Create("/../escape.txt"); err == nil {
		_, statErr := os.Stat(filepath.Join(tmp, "escape.txt"))
		if statErr == nil {
			t.Fatal("jail escape: file created outside root")
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestSFTPMounts(t *testing.T) {
	tmp := t.TempDir()
	mountDir := filepath.Join(tmp, "outside-game")
	if err := os.MkdirAll(mountDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mountDir, "game.dat"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tmp, "config.json")
	store, err := config.Open(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(c *config.Config) {
		c.RootDir = filepath.Join(tmp, "shared")
		c.Mounts = []config.Mount{
			{Name: "shared", Path: filepath.Join(tmp, "shared")},
			{Name: "Games", Path: mountDir},
		}
	}); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(tmp, "shared"), 0o755)

	port := freePort(t)
	srv, err := New(store, filepath.Join(tmp, "data"), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start("127.0.0.1", port); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	conn, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port),
		&ssh.ClientConfig{
			User:            "admin",
			Auth:            []ssh.AuthMethod{ssh.Password("admin")},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		})
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	defer conn.Close()
	c, err := sftp.NewClient(conn)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	defer c.Close()

	root, err := c.ReadDir("/")
	if err != nil {
		t.Fatalf("root list: %v", err)
	}
	names := map[string]bool{}
	for _, e := range root {
		names[e.Name()] = true
	}
	if !names["shared"] || !names["Games"] {
		t.Fatalf("mounts not listed: %v", names)
	}

	rf, err := c.Open("/Games/game.dat")
	if err != nil {
		t.Fatalf("open mounted file: %v", err)
	}
	got, err := io.ReadAll(rf)
	rf.Close()
	if err != nil || string(got) != "payload" {
		t.Fatalf("mounted file content: %q err=%v", got, err)
	}

	if _, err := c.ReadDir("/Games/../../../Windows"); err == nil {
		t.Fatal("traversal from mount allowed")
	}
}
