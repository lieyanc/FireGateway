package logx

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// rotateWriter writes to logDir/firegateway-YYYY-MM-DD.log, rotating by size and
// date, and keeps at most maxFiles log files.
type rotateWriter struct {
	mu       sync.Mutex
	dir      string
	maxSize  int64
	maxFiles int
	f        *os.File
	day      string
	size     int64
}

func newRotateWriter(dir string, maxSize int64, maxFiles int) (*rotateWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	w := &rotateWriter{dir: dir, maxSize: maxSize, maxFiles: maxFiles}
	return w, w.open()
}

func (w *rotateWriter) path() string {
	return filepath.Join(w.dir, "firegateway-"+w.day+".log")
}

func (w *rotateWriter) open() error {
	w.day = time.Now().UTC().Format(time.DateOnly)
	f, err := os.OpenFile(w.path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, st.Size()
	return nil
}

func (w *rotateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	if w.size+int64(len(p)) > w.maxSize || time.Now().UTC().Format(time.DateOnly) != w.day {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotateWriter) rotate() error {
	w.f.Close()
	if w.size >= w.maxSize {
		ts := strings.NewReplacer(":", "-", ".", "-").Replace(time.Now().UTC().Format(time.RFC3339Nano))
		os.Rename(w.path(), strings.TrimSuffix(w.path(), ".log")+"-"+ts+".log")
	}
	w.cleanup()
	return w.open()
}

func (w *rotateWriter) cleanup() {
	matches, _ := filepath.Glob(filepath.Join(w.dir, "firegateway-*.log"))
	if len(matches) < w.maxFiles {
		return
	}
	type fi struct {
		p string
		t time.Time
	}
	files := make([]fi, 0, len(matches))
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil {
			files = append(files, fi{m, st.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].t.After(files[j].t) })
	// Leave room for the file about to be opened.
	if keep := max(w.maxFiles-1, 0); len(files) > keep {
		for _, f := range files[keep:] {
			os.Remove(f.p)
		}
	}
}

func (w *rotateWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
