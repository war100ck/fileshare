package downloader

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Manifest struct {
	Name      string         `json:"name"`
	Path      string         `json:"path"`
	Generated string         `json:"generated"`
	IsDir     bool           `json:"is_dir"`
	TotalSize int64          `json:"total_size"`
	Files     []ManifestFile `json:"files"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Options struct {
	URL      string
	Out      string
	Jobs     int
	Password string
}

type client struct {
	base   string
	hc     *http.Client
	pass   string
	done   int64
	skip   int64
	failed int64
	total  int64
	start  time.Time
}

func Run(opts Options) error {
	if opts.Jobs <= 0 {
		opts.Jobs = 8
	}
	if opts.Out == "" {
		opts.Out = "."
	}
	base := strings.TrimRight(opts.URL, "/")
	manifestURL := base
	if !strings.HasSuffix(strings.ToLower(manifestURL), ".json") {
		manifestURL += "/manifest.json"
	}
	if i := strings.Index(manifestURL, "/s/"); i >= 0 {
		base = strings.TrimRight(manifestURL[:i+len("/s/")]+strings.Split(strings.TrimPrefix(manifestURL[i+len("/s/"):], "/"), "/")[0], "/")
	}

	c := &client{
		base:  base,
		hc:    &http.Client{Timeout: 0},
		pass:  opts.Password,
		start: time.Now(),
	}

	fmt.Printf("Манифест: %s\n", manifestURL)
	m, err := c.fetchManifest(manifestURL)
	if err != nil {
		return fmt.Errorf("манифест: %w", err)
	}
	fmt.Printf("Файлов: %d, всего: %s\n", len(m.Files), humanBytes(m.TotalSize))
	if len(m.Files) == 0 {
		return fmt.Errorf("манифест пуст")
	}
	if err := os.MkdirAll(opts.Out, 0o755); err != nil {
		return err
	}

	atomic.StoreInt64(&c.total, m.TotalSize)

	jobs := opts.Jobs
	if jobs > len(m.Files) {
		jobs = len(m.Files)
	}
	ch := make(chan ManifestFile)
	var wg sync.WaitGroup
	for i := 0; i < jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range ch {
				if err := c.downloadOne(opts.Out, f); err != nil {
					atomic.AddInt64(&c.failed, 1)
					fmt.Printf("\nОшибка %s: %v\n", f.Path, err)
				}
			}
		}()
	}
	stopUI := make(chan struct{})
	go c.progressUI(stopUI)
	for _, f := range m.Files {
		ch <- f
	}
	close(ch)
	wg.Wait()
	close(stopUI)

	failed := atomic.LoadInt64(&c.failed)
	skipped := atomic.LoadInt64(&c.skip)
	downloaded := atomic.LoadInt64(&c.done)
	elapsed := time.Since(c.start).Round(time.Second)
	fmt.Printf("\nГотово: %d скачано, %d пропущено, %d ошибок, время %s\n",
		downloaded, skipped, failed, elapsed)
	if failed > 0 {
		return fmt.Errorf("не все файлы скачаны (%d ошибок) — запустите ещё раз, продолжит с возобновлением", failed)
	}
	return nil
}

func (c *client) fetchManifest(u string) (*Manifest, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	if c.pass != "" {
		req.Header.Set("X-Share-Password", c.pass)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("нужен пароль ссылки (флаг -password)")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *client) downloadOne(outDir string, f ManifestFile) error {
	rel := f.Path
	if rel == "" {
		rel = filepath.Base(outDir)
	}
	rel = filepath.Clean(filepath.FromSlash(rel))
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("небезопасный путь в манифесте: %s", f.Path)
	}
	dest := filepath.Join(outDir, rel)

	if fi, err := os.Stat(dest); err == nil && fi.Size() == f.Size {
		if f.SHA256 == "" {
			atomic.AddInt64(&c.skip, 1)
			return nil
		}
		if sum, err := hashFile(dest); err == nil && strings.EqualFold(sum, f.SHA256) {
			atomic.AddInt64(&c.skip, 1)
			return nil
		}
	}

	var lastErr error
	for attempt := 1; attempt <= 5; attempt++ {
		lastErr = c.tryDownload(dest, f)
		if lastErr == nil {
			atomic.AddInt64(&c.done, f.Size)
			return nil
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return lastErr
}

func (c *client) tryDownload(dest string, f ManifestFile) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := dest + ".part"

	var offset int64
	if fi, err := os.Stat(part); err == nil {
		offset = fi.Size()
		if f.Size > 0 && offset > f.Size {
			offset = 0
		}
	}

	fileURL := c.base + "/file/" + escapePath(f.Path)
	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		return err
	}
	if c.pass != "" {
		req.Header.Set("X-Share-Password", c.pass)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		offset = 0
	case http.StatusPartialContent:
	case http.StatusUnauthorized:
		return fmt.Errorf("нужен пароль ссылки (флаг -password)")
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	out, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}

	buf := make([]byte, 256<<10)
	var written int64 = offset
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return werr
			}
			written += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}

	if f.Size > 0 && written != f.Size {
		return fmt.Errorf("размер не совпал: %d != %d", written, f.Size)
	}
	if f.SHA256 != "" {
		sum, err := hashFile(part)
		if err != nil {
			return err
		}
		if !strings.EqualFold(sum, f.SHA256) {
			os.Remove(part)
			return fmt.Errorf("контрольная сумма не совпала")
		}
	}
	os.Remove(dest)
	if err := os.Rename(part, dest); err != nil {
		return err
	}
	return nil
}

func (c *client) progressUI(stop <-chan struct{}) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	var lastDone int64
	lastTime := time.Now()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			done := atomic.LoadInt64(&c.done)
			total := atomic.LoadInt64(&c.total)
			speed := float64(done-lastDone) / now.Sub(lastTime).Seconds()
			lastDone = done
			lastTime = now
			pct := 0.0
			if total > 0 {
				pct = float64(done) / float64(total) * 100
			}
			eta := "?"
			if speed > 0 && total > done {
				eta = (time.Duration(float64(total-done)/speed) * time.Second).Round(time.Second).String()
			}
			fmt.Printf("\r%5.1f%%  %s / %s  %s/с  осталось %s    ",
				pct, humanBytes(done), humanBytes(total), humanBytes(int64(speed)), eta)
		}
	}
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d Б", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cБ", float64(n)/float64(div), "КМГТ"[exp])
}
