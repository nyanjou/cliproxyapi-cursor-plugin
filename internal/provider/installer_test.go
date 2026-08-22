package provider

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestStartLoginMissingAgentReturnsSameOriginSetupURL(t *testing.T) {
	s := newTestService(t, filepath.Join(t.TempDir(), "missing-agent"))
	resp, err := s.StartLogin(context.Background(), "", pluginapi.AuthLoginStartRequest{BaseURL: "https://proxy.example.test/ui/auth?next=https://evil.test"})
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	if resp.URL != "https://proxy.example.test/v0/resource/plugins/cursor/setup" {
		t.Fatalf("setup URL = %q", resp.URL)
	}
	if resp.Metadata["setup_required"] != true {
		t.Fatalf("metadata missing setup_required: %#v", resp.Metadata)
	}
}

func TestStartLoginRejectsUntrustedBaseURLWhenSetupRequired(t *testing.T) {
	s := newTestService(t, filepath.Join(t.TempDir(), "missing-agent"))
	_, err := s.StartLogin(context.Background(), "", pluginapi.AuthLoginStartRequest{BaseURL: "javascript:alert(1)"})
	if err == nil || !strings.Contains(err.Error(), "trusted BaseURL") {
		t.Fatalf("expected trusted BaseURL error, got %v", err)
	}
}

func TestLoginApprovalURLIsReturnedBeforeAgentExits(t *testing.T) {
	agent := fakeAgent(t, `
if [ "$1" = "status" ]; then echo 'Not logged in'; exit 0; fi
if [ "$1" = "login" ]; then
  printf 'Open https://cursor.com/login/device?user_code=SAFE-CODE\n'
  sleep 2
  exit 0
fi
exit 64
`)
	s := newTestService(t, agent)
	start := time.Now()
	resp, err := s.StartLogin(context.Background(), "", pluginapi.AuthLoginStartRequest{BaseURL: "http://127.0.0.1:8080/"})
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("StartLogin waited for process exit")
	}
	if resp.URL != "https://cursor.com/login/device?user_code=SAFE-CODE" {
		t.Fatalf("approval URL = %q metadata=%#v", resp.URL, resp.Metadata)
	}
	poll, err := s.PollLogin(context.Background(), "", resp.State)
	if err != nil {
		t.Fatalf("PollLogin: %v", err)
	}
	if poll.Status != pluginapi.AuthLoginStatusPending || !strings.Contains(poll.Message, resp.URL) {
		t.Fatalf("poll = %#v", poll)
	}
}

func TestManagementRegistrationSeparatesResourceAndAuthenticatedInstall(t *testing.T) {
	s := newTestService(t, fakeAgent(t, `exit 0`))
	reg, err := s.RegisterManagement(context.Background(), pluginapi.ManagementRegistrationRequest{ResourceBasePath: "/v0/resource/plugins/cursor", BasePath: "/v0/management/plugins/cursor"})
	if err != nil {
		t.Fatalf("RegisterManagement: %v", err)
	}
	if len(reg.Resources) != 1 || reg.Resources[0].Path != "/setup" {
		t.Fatalf("resources = %#v", reg.Resources)
	}
	installRoutes := 0
	for _, r := range reg.Routes {
		if r.Method == http.MethodPost && r.Path == "/plugins/cursor/setup/install" {
			installRoutes++
		}
	}
	if installRoutes != 1 {
		t.Fatalf("install route not registered exactly once: %#v", reg.Routes)
	}
}

func TestParseOfficialInstallerStrictly(t *testing.T) {
	ok := []byte(`VERSION="2026.08.11-e8db854"
url="https://downloads.cursor.com/lab/2026.08.11-e8db854/linux/x64/agent-cli-package.tar.gz"`)
	info, err := parseOfficialInstaller(ok, "linux", "amd64")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Version != "2026.08.11-e8db854" || info.PackageURL != "https://downloads.cursor.com/lab/2026.08.11-e8db854/linux/x64/agent-cli-package.tar.gz" {
		t.Fatalf("info=%#v", info)
	}
	bad := [][]byte{
		[]byte(`https://evil.test/lab/2026.08.11-e8db854/linux/x64/agent-cli-package.tar.gz`),
		[]byte(`https://downloads.cursor.com/lab/../../linux/x64/agent-cli-package.tar.gz`),
		[]byte(string(ok) + "\n" + string(ok)),
	}
	for _, b := range bad {
		if _, err := parseOfficialInstaller(b, "linux", "amd64"); err == nil {
			t.Fatalf("expected reject for %s", b)
		}
	}
}

func TestSafeExtractRejectsTraversalLinksAndDevices(t *testing.T) {
	cases := map[string]func(*tar.Writer){
		"traversal": func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Name: "../agent", Mode: 0o755, Size: 1})
			_, _ = tw.Write([]byte("x"))
		},
		"absolute": func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Name: "/agent", Mode: 0o755, Size: 1})
			_, _ = tw.Write([]byte("x"))
		},
		"symlink escape": func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Name: "bin/agent", Typeflag: tar.TypeSymlink, Linkname: "../../evil"})
		},
		"hardlink": func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Name: "bin/agent", Typeflag: tar.TypeLink, Linkname: "cursor-agent"})
		},
		"device": func(tw *tar.Writer) { _ = tw.WriteHeader(&tar.Header{Name: "dev", Typeflag: tar.TypeChar}) },
	}
	for name, makeTar := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := safeExtractTarGz(bytes.NewReader(tarGz(t, makeTar)), t.TempDir(), extractLimits{MaxEntries: 20, MaxExpandedBytes: 1024 * 1024})
			if err == nil {
				t.Fatal("expected extraction rejection")
			}
		})
	}
}

func TestInstallOfficialCursorAgentIsExplicitSerializedAndIdempotent(t *testing.T) {
	var downloads int
	var mu sync.Mutex
	pkg := tarGz(t, func(tw *tar.Writer) {
		body := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'Cursor Agent 2026.08.11-e8db854'; exit 0; fi\nexit 0\n"
		_ = tw.WriteHeader(&tar.Header{Name: "cursor-agent", Mode: 0o755, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		downloads++
		mu.Unlock()
		switch r.URL.Path {
		case "/install":
			_, _ = w.Write([]byte(`https://downloads.cursor.com/lab/2026.08.11-e8db854/linux/x64/agent-cli-package.tar.gz`))
		case "/pkg":
			_, _ = w.Write(pkg)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	home := t.TempDir()
	s := newTestService(t, fakeAgent(t, `exit 0`))
	cfg := s.Config()
	cfg.InstallerURL = ts.URL + "/install"
	cfg.TestPackageURLOverride = ts.URL + "/pkg"
	cfg.ManagedInstallRoot = home
	originalExecutable := cfg.ExecutablePath
	if err := s.Configure(mustYAML(t, cfg)); err != nil {
		t.Fatal(err)
	}
	if st := s.InstallStatus(); st.Installed {
		t.Fatalf("installed before explicit install: %#v", st)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.InstallOfficialCursorAgent(context.Background()); err != nil {
				t.Errorf("install: %v", err)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	gotDownloads := downloads
	mu.Unlock()
	if gotDownloads != 2 { // one installer script + one package; concurrent calls reuse installed result
		t.Fatalf("downloads=%d", gotDownloads)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "agent")); err != nil {
		t.Fatalf("agent symlink missing: %v", err)
	}
	if s.Config().ExecutablePath != originalExecutable {
		t.Fatalf("installer mutated configured executable")
	}
}

func tarGz(t *testing.T, writeTar func(*tar.Writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	writeTar(tw)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestManagementInstallHandlerRequiresConfirm(t *testing.T) {
	s := newTestService(t, fakeAgent(t, `exit 0`))
	resp, err := s.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: http.MethodPost, Path: "/plugins/cursor/setup/install", Body: []byte(`{"confirm":false}`)})
	if err != nil {
		t.Fatalf("HandleManagement: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(resp.Body), "explicit") {
		t.Fatalf("response=%d %s", resp.StatusCode, resp.Body)
	}
	var body map[string]any
	_ = json.Unmarshal(resp.Body, &body)
	if body["installed"] == true {
		t.Fatalf("installed on unconfirmed request: %s", resp.Body)
	}
}
