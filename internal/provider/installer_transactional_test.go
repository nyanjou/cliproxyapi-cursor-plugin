package provider

import (
	"archive/tar"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallOfficialCursorAgentCurrentSwapFailurePreservesPreviousVersion(t *testing.T) {
	home := t.TempDir()
	s, srv := transactionalInstallService(t, home)
	defer srv.close()

	srv.version = "2026.08.11-e8db854"
	srv.pkg = cursorAgentPackage(t, srv.version)
	if _, err := s.InstallOfficialCursorAgent(context.Background()); err != nil {
		t.Fatalf("initial install: %v", err)
	}

	srv.version = "2026.08.12-deadbee"
	srv.pkg = cursorAgentPackage(t, srv.version)
	restore := failNextInstallerRenameTo(t, filepath.Join(home, ".local", "share", "cursor-agent", "current"), errors.New("injected current swap failure"))
	_, err := s.InstallOfficialCursorAgent(context.Background())
	restore()
	if err == nil || !strings.Contains(err.Error(), "injected current swap failure") {
		t.Fatalf("expected injected current swap failure, got %v", err)
	}
	assertInstalledVersion(t, s, home, "2026.08.11-e8db854")
}

func TestInstallOfficialCursorAgentInitialCurrentSwapFailureLeavesNoInstalledBins(t *testing.T) {
	home := t.TempDir()
	s, srv := transactionalInstallService(t, home)
	defer srv.close()
	srv.version = "2026.08.11-e8db854"
	srv.pkg = cursorAgentPackage(t, srv.version)

	restore := failNextInstallerRenameTo(t, filepath.Join(home, ".local", "share", "cursor-agent", "current"), errors.New("injected initial current swap failure"))
	_, err := s.InstallOfficialCursorAgent(context.Background())
	restore()
	if err == nil || !strings.Contains(err.Error(), "injected initial current swap failure") {
		t.Fatalf("expected injected current swap failure, got %v", err)
	}
	if st := s.InstallStatus(); st.Installed || st.Version != "" {
		t.Fatalf("status should remain uninstalled, got %#v", st)
	}
	for _, name := range []string{"agent", "cursor-agent"} {
		link := filepath.Join(home, ".local", "bin", name)
		if _, statErr := os.Stat(link); statErr == nil {
			t.Fatalf("%s should not look installed after failed initial activation", name)
		} else if !os.IsNotExist(statErr) {
			t.Fatalf("%s unexpected stat error: %v", name, statErr)
		}
	}
}

func TestInstallOfficialCursorAgentSecondBinLinkFailurePreservesPreviousExecutables(t *testing.T) {
	home := t.TempDir()
	s, srv := transactionalInstallService(t, home)
	defer srv.close()
	srv.version = "2026.08.11-e8db854"
	srv.pkg = cursorAgentPackage(t, srv.version)
	if _, err := s.InstallOfficialCursorAgent(context.Background()); err != nil {
		t.Fatalf("initial install: %v", err)
	}

	srv.version = "2026.08.12-deadbee"
	srv.pkg = cursorAgentPackage(t, srv.version)
	cursorAgentBin := filepath.Join(home, ".local", "bin", "cursor-agent")
	restore := failNextInstallerRenameTo(t, cursorAgentBin, errors.New("injected second link failure"))
	_, err := s.InstallOfficialCursorAgent(context.Background())
	restore()
	if err == nil || !strings.Contains(err.Error(), "injected second link failure") {
		t.Fatalf("expected injected second link failure, got %v", err)
	}
	assertInstalledVersion(t, s, home, "2026.08.11-e8db854")
}

func TestInstallOfficialCursorAgentMigratesPriorDirectManagedSymlinks(t *testing.T) {
	home := t.TempDir()
	previous := "2026.08.11-e8db854"
	archiveRel := filepath.Join("dist-package", "cursor-agent")
	previousBin := filepath.Join(home, ".local", "share", "cursor-agent", "versions", previous, archiveRel)
	writeVersionScript(t, previousBin, previous)
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	directTarget, err := filepath.Rel(binDir, previousBin)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent", "cursor-agent"} {
		if err := os.Symlink(directTarget, filepath.Join(binDir, name)); err != nil {
			t.Fatal(err)
		}
	}

	s, srv := transactionalInstallService(t, home)
	defer srv.close()
	srv.version = "2026.08.12-deadbee"
	srv.pkg = cursorAgentPackage(t, srv.version)
	if _, err := s.InstallOfficialCursorAgent(context.Background()); err != nil {
		t.Fatalf("upgrade from direct-link layout: %v", err)
	}
	assertInstalledVersion(t, s, home, srv.version)
	for _, name := range []string{"agent", "cursor-agent"} {
		target, err := os.Readlink(filepath.Join(binDir, name))
		if err != nil {
			t.Fatalf("Readlink(%s): %v", name, err)
		}
		if !strings.Contains(filepath.ToSlash(target), "/current/") {
			t.Fatalf("%s was not migrated to current indirection: %q", name, target)
		}
	}
}

func TestInstallOfficialCursorAgentFailedDirectLinkMigrationPreservesPreviousExecutables(t *testing.T) {
	home := t.TempDir()
	previous := "2026.08.11-e8db854"
	archiveRel := filepath.Join("dist-package", "cursor-agent")
	previousBin := filepath.Join(home, ".local", "share", "cursor-agent", "versions", previous, archiveRel)
	writeVersionScript(t, previousBin, previous)
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	directTarget, err := filepath.Rel(binDir, previousBin)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent", "cursor-agent"} {
		if err := os.Symlink(directTarget, filepath.Join(binDir, name)); err != nil {
			t.Fatal(err)
		}
	}

	s, srv := transactionalInstallService(t, home)
	defer srv.close()
	srv.version = "2026.08.12-deadbee"
	srv.pkg = cursorAgentPackage(t, srv.version)
	restore := failNextInstallerRenameTo(t, filepath.Join(binDir, "cursor-agent"), errors.New("injected direct migration failure"))
	_, err = s.InstallOfficialCursorAgent(context.Background())
	restore()
	if err == nil || !strings.Contains(err.Error(), "injected direct migration failure") {
		t.Fatalf("expected injected migration failure, got %v", err)
	}
	assertInstalledVersion(t, s, home, previous)
}

func TestInstallOfficialCursorAgentInitialActivationFailureCleansTemporaryArtifacts(t *testing.T) {
	home := t.TempDir()
	s, srv := transactionalInstallService(t, home)
	defer srv.close()
	srv.version = "2026.08.11-e8db854"
	srv.pkg = cursorAgentPackage(t, srv.version)
	restore := failNextInstallerRenameTo(t, filepath.Join(home, ".local", "share", "cursor-agent", "current"), errors.New("injected cleanup failure"))
	_, err := s.InstallOfficialCursorAgent(context.Background())
	restore()
	if err == nil {
		t.Fatal("expected activation failure")
	}
	matches, err := filepath.Glob(filepath.Join(home, ".cursor-agent-install-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary install dirs were not cleaned: %v", matches)
	}
	versions, err := filepath.Glob(filepath.Join(home, ".local", "share", "cursor-agent", "versions", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 0 {
		t.Fatalf("inactive version artifacts were not cleaned: %v", versions)
	}
}

func TestLiveOfficialPackageUpgradeProbe(t *testing.T) {
	if os.Getenv("CURSOR_LIVE_PACKAGE_UPGRADE_PROBE") != "1" {
		t.Skip("set CURSOR_LIVE_PACKAGE_UPGRADE_PROBE=1 to upgrade a disposable prior install to the live official Cursor package")
	}
	home := t.TempDir()
	previous := "2026.08.10-oldfake"
	archiveRel := filepath.Join("dist-package", "cursor-agent")
	previousBin := filepath.Join(home, ".local", "share", "cursor-agent", "versions", previous, archiveRel)
	writeVersionScript(t, previousBin, previous)
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	directTarget, err := filepath.Rel(binDir, previousBin)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent", "cursor-agent"} {
		if err := os.Symlink(directTarget, filepath.Join(binDir, name)); err != nil {
			t.Fatal(err)
		}
	}

	s := newTestService(t, fakeAgent(t, `exit 0`))
	cfg := s.Config()
	cfg.InstallerURL = officialInstallerURL
	cfg.TestPackageURLOverride = ""
	cfg.ManagedInstallRoot = home
	cfg.MaxPackageBytes = 256 * 1024 * 1024
	cfg.MaxExpandedBytes = 512 * 1024 * 1024
	cfg.MaxArchiveEntries = 10000
	if err := s.Configure(mustYAML(t, cfg)); err != nil {
		t.Fatal(err)
	}
	assertInstalledVersion(t, s, home, previous)
	res, err := s.InstallOfficialCursorAgent(context.Background())
	if err != nil {
		t.Fatalf("live official package upgrade: %v", err)
	}
	if !res.Installed || res.Version == "" || res.Version == previous || res.PackageSHA256 == "" || res.PackageBytes <= 0 {
		t.Fatalf("result=%#v", res)
	}
	assertInstalledVersion(t, s, home, res.Version)
	for _, name := range []string{"agent", "cursor-agent"} {
		target, err := os.Readlink(filepath.Join(binDir, name))
		if err != nil {
			t.Fatalf("Readlink(%s): %v", name, err)
		}
		if !strings.Contains(filepath.ToSlash(target), "/current/") {
			t.Fatalf("%s was not migrated to current indirection after live upgrade: %q", name, target)
		}
	}
	t.Logf("upgraded_from=%s version=%s package_sha256=%s package_bytes=%d", previous, res.Version, res.PackageSHA256, res.PackageBytes)
}

func TestInstallOfficialCursorAgentRejectsEscapingManagedCurrentLink(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "cursor-agent")
	writeVersionScript(t, outside, "2026.08.11-e8db854")
	current := filepath.Join(home, ".local", "share", "cursor-agent", "current")
	if err := os.MkdirAll(filepath.Dir(current), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, current); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent", "cursor-agent"} {
		if err := os.Symlink(filepath.Join("..", "share", "cursor-agent", "current"), filepath.Join(binDir, name)); err != nil {
			t.Fatal(err)
		}
	}

	s := newTestService(t, fakeAgent(t, `exit 0`))
	cfg := s.Config()
	cfg.ManagedInstallRoot = home
	if err := s.Configure(mustYAML(t, cfg)); err != nil {
		t.Fatal(err)
	}
	if st := s.InstallStatus(); st.Installed || !strings.Contains(st.Error, "escapes managed versions") {
		t.Fatalf("expected escaping current link to be rejected, got %#v", st)
	}
}

type transactionalInstallServer struct {
	version string
	pkg     []byte
	close   func()
}

func transactionalInstallService(t *testing.T, home string) (*Service, *transactionalInstallServer) {
	t.Helper()
	srv := &transactionalInstallServer{version: "2026.08.11-e8db854"}
	srv.pkg = cursorAgentPackage(t, srv.version)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/install":
			_, _ = w.Write([]byte(`DOWNLOAD_URL="https://downloads.cursor.com/lab/` + srv.version + `/${OS}/${ARCH}/agent-cli-package.tar.gz"`))
		case "/pkg":
			_, _ = w.Write(srv.pkg)
		default:
			http.NotFound(w, r)
		}
	}))
	srv.close = ts.Close
	s := newTestService(t, fakeAgent(t, `exit 0`))
	cfg := s.Config()
	cfg.InstallerURL = ts.URL + "/install"
	cfg.TestPackageURLOverride = ts.URL + "/pkg"
	cfg.ManagedInstallRoot = home
	if err := s.Configure(mustYAML(t, cfg)); err != nil {
		t.Fatal(err)
	}
	return s, srv
}

func cursorAgentPackage(t *testing.T, version string) []byte {
	t.Helper()
	return tarGz(t, func(tw *tar.Writer) {
		body := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'Cursor Agent " + version + "'; exit 0; fi\nexit 0\n"
		_ = tw.WriteHeader(&tar.Header{Name: "dist-package", Typeflag: tar.TypeDir, Mode: 0o755})
		_ = tw.WriteHeader(&tar.Header{Name: "dist-package/cursor-agent", Mode: 0o755, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	})
}

func writeVersionScript(t *testing.T, path, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'Cursor Agent " + version + "'; exit 0; fi\nexit 0\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertInstalledVersion(t *testing.T, s *Service, home, version string) {
	t.Helper()
	if st := s.InstallStatus(); !st.Installed || st.Version != version {
		t.Fatalf("status=%#v, want installed version %s", st, version)
	}
	for _, name := range []string{"agent", "cursor-agent"} {
		link := filepath.Join(home, ".local", "bin", name)
		out, err := exec.Command(link, "--version").CombinedOutput()
		if err != nil || !strings.Contains(string(out), version) {
			t.Fatalf("%s --version output=%q err=%v, want %s", name, out, err, version)
		}
	}
}

func failNextInstallerRenameTo(t *testing.T, target string, err error) func() {
	t.Helper()
	orig := installerRename
	failed := false
	installerRename = func(oldpath, newpath string) error {
		if !failed && newpath == target {
			failed = true
			return err
		}
		return orig(oldpath, newpath)
	}
	return func() { installerRename = orig }
}
