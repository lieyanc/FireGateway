package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const LevelTrace = slog.Level(-8)

var levelNames = map[string]slog.Level{
	"error": slog.LevelError,
	"warn":  slog.LevelWarn,
	"info":  slog.LevelInfo,
	"debug": slog.LevelDebug,
	"trace": LevelTrace,
}

var logLevel = new(slog.LevelVar)

func parseLevel(s string) (slog.Level, bool) {
	l, ok := levelNames[strings.ToLower(s)]
	return l, ok
}

func levelName(l slog.Level) string {
	for k, v := range levelNames {
		if v == l {
			return k
		}
	}
	return l.String()
}

func setupLogger(c LogConfig) (io.Closer, error) {
	if l, ok := parseLevel(c.Level); ok {
		logLevel.Set(l)
	} else {
		logLevel.Set(slog.LevelInfo)
	}
	opts := &slog.HandlerOptions{
		Level: logLevel,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if l, ok := a.Value.Any().(slog.Level); ok && a.Key == slog.LevelKey && l == LevelTrace {
				a.Value = slog.StringValue("TRACE")
			}
			return a
		},
	}

	var handlers []slog.Handler
	if boolOr(c.EnableConsole, true) {
		handlers = append(handlers, slog.NewTextHandler(os.Stdout, opts))
	}
	var closer io.Closer = io.NopCloser(nil)
	if boolOr(c.EnableFile, false) {
		rw, err := newRotateWriter(c.LogDir, c.MaxFileSize, c.MaxFiles)
		if err != nil {
			return nil, err
		}
		handlers = append(handlers, slog.NewJSONHandler(rw, opts))
		closer = rw
	}

	var h slog.Handler
	switch len(handlers) {
	case 0:
		h = slog.DiscardHandler
	case 1:
		h = handlers[0]
	default:
		h = multiHandler(handlers)
	}
	slog.SetDefault(slog.New(h))
	return closer, nil
}

type multiHandler []slog.Handler

func (m multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range m {
		if h.Enabled(ctx, r.Level) {
			errs = append(errs, h.Handle(ctx, r.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (m multiHandler) WithAttrs(a []slog.Attr) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithAttrs(a)
	}
	return out
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithGroup(name)
	}
	return out
}

// rotateWriter writes to logDir/fireproxy-YYYY-MM-DD.log, rotating by size and
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
	return filepath.Join(w.dir, "fireproxy-"+w.day+".log")
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
	matches, _ := filepath.Glob(filepath.Join(w.dir, "fireproxy-*.log"))
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
