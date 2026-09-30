package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lieyanc/FireGateway/internal/updater"
)

func TestUpdateApplyForceRequiresAuthentication(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.do(t, "POST", "/api/update/apply", `{"force":true}`); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated force apply: %d", code)
	}
	h.setup(t)
	if code, _ := h.do(t, "POST", "/api/update/apply", `{"force":true}`); code != http.StatusConflict {
		t.Fatalf("force apply without a verified update: %d", code)
	}
}

func TestUpdateApplyRejectsInvalidOptions(t *testing.T) {
	s := &Server{Deps: Deps{Updater: updater.New(func() updater.Config { return updater.Config{} }, nil, nil, updater.RestartHooks{})}}
	for _, body := range []string{`{`, `[]`, `{"force":"true"}`, `{"force":true} {}`, `{"force":true} garbage`, strings.Repeat(" ", 1025) + `{}`} {
		t.Run(body[:min(len(body), 40)], func(t *testing.T) {
			w := httptest.NewRecorder()
			s.updateApply(w, httptest.NewRequest("POST", "/api/update/apply", strings.NewReader(body)))
			if w.Code != http.StatusBadRequest || s.Updater.Status().State != "idle" {
				t.Fatalf("invalid options started update: code=%d status=%+v", w.Code, s.Updater.Status())
			}
		})
	}
}

func TestUpdateApplyForceAcrossChannels(t *testing.T) {
	for _, tc := range []struct {
		name, channel, startBody string
		forceReady               bool
	}{
		{"stable waiting", "stable", "", false},
		{"dev waiting", "dev", `{"force":false}`, false},
		{"dev ready", "dev", `{}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := []byte("verified test binary")
			target := "firegateway-" + runtime.GOOS + "-" + runtime.GOARCH
			if runtime.GOOS == "windows" {
				target += ".exe"
			}
			tag := "v99.0.0"
			if tc.channel == "dev" {
				tag = "dev"
			}
			var downloads, restarts atomic.Int32
			mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/releases/owner/repo/latest", "/api/releases/owner/repo/dev":
					assets := []map[string]string{}
					for _, name := range []string{target, updater.SHA256SUMSName, "version.json"} {
						assets = append(assets, map[string]string{"name": name, "browser_download_url": "http://" + r.Host + "/" + name})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "prerelease": tc.channel == "dev", "assets": assets})
				case "/version.json":
					_ = json.NewEncoder(w).Encode(map[string]string{"version": "dev-9999-20260930-bbbbbbb", "commit": "bbbbbbb", "tag": tag})
				case "/" + target:
					downloads.Add(1)
					_, _ = w.Write(binary)
				case "/" + updater.SHA256SUMSName:
					_, _ = fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(binary), target)
				default:
					t.Errorf("unexpected update request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer mirror.Close()
			dataDir := t.TempDir()
			u := updater.New(func() updater.Config {
				return updater.Config{Channel: tc.channel, Source: updater.SourceProxy, ProxyBaseURL: mirror.URL, Repo: "owner/repo"}
			}, func() string { return dataDir }, nil, updater.RestartHooks{
				IsBusy: func() bool { return true },
				BeforeExec: func(gotTag string) error {
					restarts.Add(1)
					if gotTag != tag {
						t.Errorf("restart tag = %q, want %q", gotTag, tag)
					}
					return fmt.Errorf("test stops before exec")
				},
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			u.StartBackground(ctx)
			s := &Server{Deps: Deps{Updater: u}}
			apply := func(body string, wantCode int) {
				t.Helper()
				r := httptest.NewRequest("POST", "/api/update/apply", strings.NewReader(body))
				requestCtx, cancelRequest := context.WithCancel(r.Context())
				w := httptest.NewRecorder()
				s.updateApply(w, r.WithContext(requestCtx))
				cancelRequest()
				if w.Code != wantCode {
					t.Fatalf("apply %s: code=%d body=%s", body, w.Code, w.Body.String())
				}
			}
			waitState := func(state string) {
				t.Helper()
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					if u.Status().State == state {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatalf("expected %s, got %+v", state, u.Status())
			}
			apply(tc.startBody, http.StatusAccepted)
			if tc.channel == "dev" {
				waitState("ready")
				if !tc.forceReady {
					apply("", http.StatusAccepted)
				}
			}
			if !tc.forceReady {
				waitState("waiting")
				apply(`{"force":false}`, http.StatusConflict)
			}
			if restarts.Load() != 0 {
				t.Fatal("normal update restarted with active connections")
			}
			apply(`{"force":true}`, http.StatusAccepted)
			waitState("failed")
			if !strings.Contains(u.Status().Error, "prepare restart") || restarts.Load() != 1 || downloads.Load() != 1 {
				t.Fatalf("expected one download and restart: downloads=%d restarts=%d status=%+v", downloads.Load(), restarts.Load(), u.Status())
			}
		})
	}
}
