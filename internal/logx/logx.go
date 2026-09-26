// Package logx configures slog with console/file output and keeps recent
// records in memory for the web UI.
package logx

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/lieyanc/FireGateway/internal/config"
)

const LevelTrace = slog.Level(-8)

// Levels lists level names from most to least severe.
var Levels = []string{"error", "warn", "info", "debug", "trace"}

var levelNames = map[string]slog.Level{
	"error": slog.LevelError,
	"warn":  slog.LevelWarn,
	"info":  slog.LevelInfo,
	"debug": slog.LevelDebug,
	"trace": LevelTrace,
}

// Level is the process-wide minimum level, adjustable at runtime.
var Level = new(slog.LevelVar)

// Recent holds the most recent records for the UI.
var Recent = NewHub(2000)

func ParseLevel(s string) (slog.Level, bool) {
	l, ok := levelNames[strings.ToLower(strings.TrimSpace(s))]
	return l, ok
}

func LevelName(l slog.Level) string {
	for k, v := range levelNames {
		if v == l {
			return k
		}
	}
	return strings.ToLower(l.String())
}

// levelLabel is the upper-case label used in output, with TRACE for LevelTrace.
func levelLabel(l slog.Level) string {
	if l <= LevelTrace {
		return "TRACE"
	}
	return l.String()
}

// Setup installs the default logger. The returned closer flushes the log file.
func Setup(c config.LogConfig) (io.Closer, error) {
	if l, ok := ParseLevel(c.Level); ok {
		Level.Set(l)
	} else {
		Level.Set(slog.LevelInfo)
	}
	opts := &slog.HandlerOptions{
		Level: Level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if l, ok := a.Value.Any().(slog.Level); ok && a.Key == slog.LevelKey {
				a.Value = slog.StringValue(levelLabel(l))
			}
			return a
		},
	}

	handlers := []slog.Handler{&ringHandler{hub: Recent}}
	if c.EnableConsole {
		handlers = append(handlers, slog.NewTextHandler(os.Stdout, opts))
	}
	var closer io.Closer = io.NopCloser(nil)
	if c.EnableFile {
		rw, err := newRotateWriter(c.LogDir, c.MaxFileSize, c.MaxFiles)
		if err != nil {
			return nil, err
		}
		handlers = append(handlers, slog.NewJSONHandler(rw, opts))
		closer = rw
	}
	slog.SetDefault(slog.New(multiHandler(handlers)))
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
