package web

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var storeExts = map[string]bool{
	".zip": true, ".gz": true, ".bz2": true, ".xz": true, ".zst": true,
	".7z": true, ".rar": true, ".png": true, ".jpg": true, ".jpeg": true,
	".gif": true, ".webp": true, ".mp3": true, ".ogg": true, ".opus": true,
	".mp4": true, ".webm": true, ".mkv": true, ".avi": true, ".mov": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".ico": true,
	".wasm": true, ".jar": true, ".apk": true,
}

func streamZip(w http.ResponseWriter, filename, absPath string, maxBytes int64) error {
	st, err := os.Stat(absPath)
	if err != nil {
		return err
	}
	var base string
	if st.IsDir() {
		base = absPath
	} else {
		base = filepath.Dir(absPath)
	}
	type zinfo struct {
		rel  string
		info fs.FileInfo
	}
	var entries []zinfo
	var total int64
	err = filepath.Walk(absPath, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if info.IsDir() {
			entries = append(entries, zinfo{rel: filepath.ToSlash(rel) + "/", info: info})
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		total += info.Size()
		if maxBytes > 0 && total > maxBytes {
			return fmt.Errorf("too big")
		}
		entries = append(entries, zinfo{rel: filepath.ToSlash(rel), info: info})
		return nil
	})
	if err != nil {
		if err.Error() == "too big" {
			return errTooBig
		}
		return err
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", disposition(filename+".zip"))
	w.WriteHeader(http.StatusOK)

	zw := zip.NewWriter(w)
	for _, e := range entries {
		hdr, err := zip.FileInfoHeader(e.info)
		if err != nil {
			return err
		}
		hdr.Name = e.rel
		if e.info.IsDir() {
			hdr.Name += "/"
			hdr.Method = zip.Store
			if _, err := zw.CreateHeader(hdr); err != nil {
				return err
			}
			continue
		}
		if storeExts[strings.ToLower(filepath.Ext(e.info.Name()))] {
			hdr.Method = zip.Store
		} else {
			hdr.Method = zip.Deflate
		}
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(filepath.Join(base, filepath.FromSlash(strings.TrimSuffix(e.rel, "/"))))
		if err != nil {
			return err
		}
		_, err = io.Copy(fw, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	return zw.Close()
}

type tooBigError struct{}

func (tooBigError) Error() string { return "папка больше лимита ZIP" }

var errTooBig = tooBigError{}
