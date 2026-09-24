package vfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fileshare/internal/config"
)

func TestResolveLegacy(t *testing.T) {
	root := t.TempDir()
	abs, err := Resolve(nil, root, "a/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(abs, filepath.Clean(root)) {
		t.Fatalf("outside root: %s", abs)
	}
	if _, err := Resolve(nil, root, "../../etc"); err == nil {
		t.Fatal("traversal allowed")
	}
}

func TestResolveMounts(t *testing.T) {
	d1 := t.TempDir()
	d2 := t.TempDir()
	mounts := []config.Mount{
		{Name: "Игры", Path: d1},
		{Name: "Med", Path: d2},
	}
	abs, err := Resolve(mounts, "", "Игры/sub/x.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(abs, filepath.Clean(d1)) {
		t.Fatalf("wrong mount: %s", abs)
	}
	if _, err := Resolve(mounts, "", "nope/x"); err == nil {
		t.Fatal("unknown mount allowed")
	}
	if _, err := Resolve(mounts, "", "Игры/../../../win"); err == nil {
		t.Fatal("traversal allowed")
	}
	if !IsWithin(mounts, "", filepath.Join(d2, "ok")) {
		t.Fatal("IsWithin d2 false")
	}
	if IsWithin(mounts, "", `C:\Windows\System32`) {
		t.TempDir()
		if _, e := os.Stat(`C:\Windows\System32`); e == nil {
			t.Fatal("IsWithin allowed system dir")
		}
	}
}
