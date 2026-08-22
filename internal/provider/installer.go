package provider

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	officialInstallerURL  = "https://cursor.com/install"
	setupResourcePath     = "/v0/resource/plugins/cliproxyapi-cursor/setup"
	managementBasePath    = "/v0/management"
	managementInstallPath = "/plugins/cursor/setup/install"
	managementStatusPath  = "/plugins/cursor/setup/status"
)

type installerInfo struct {
	Version    string
	PackageURL string
}

type InstallStatus struct {
	Installed      bool   `json:"installed"`
	Version        string `json:"version,omitempty"`
	ExecutablePath string `json:"executable_path,omitempty"`
	ManagedRoot    string `json:"managed_root,omitempty"`
	Error          string `json:"error,omitempty"`
}

type InstallResult struct {
	Installed      bool   `json:"installed"`
	Version        string `json:"version"`
	ExecutablePath string `json:"executable_path"`
	PackageSHA256  string `json:"package_sha256"`
	PackageBytes   int64  `json:"package_bytes"`
}

type extractLimits struct {
	MaxEntries       int
	MaxExpandedBytes int64
}

type extractedArchive struct {
	BinaryPath                string
	ArchiveRelativeBinaryPath string
}

var (
	officialPkgCandidateRE = regexp.MustCompile(`https?://[^\s"'<>]+/agent-cli-package\.tar\.gz`)
	officialPkgTemplateRE  = regexp.MustCompile(`^https://downloads\.cursor\.com/lab/([0-9]{4}\.[0-9]{2}\.[0-9]{2}-[A-Za-z0-9][A-Za-z0-9._-]{2,63})/\$\{OS\}/\$\{ARCH\}/agent-cli-package\.tar\.gz$`)
)

func setupURLFromBase(base string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u == nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("trusted BaseURL is required for Cursor setup URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("trusted BaseURL must be http or https")
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: setupResourcePath}).String(), nil
}

func parseOfficialInstaller(body []byte, goos, goarch string) (installerInfo, error) {
	if len(body) == 0 || len(body) > 128*1024 {
		return installerInfo{}, fmt.Errorf("official installer body size is invalid")
	}
	wantOS, wantArch, ok := installerPlatform(goos, goarch)
	if !ok {
		return installerInfo{}, fmt.Errorf("unsupported Cursor Agent CLI platform %s/%s", goos, goarch)
	}
	candidates := officialPkgCandidateRE.FindAll(body, -1)
	if len(candidates) != 1 {
		return installerInfo{}, fmt.Errorf("official installer must contain exactly one Cursor package URL candidate for this parser; found %d", len(candidates))
	}
	m := officialPkgTemplateRE.FindSubmatch(candidates[0])
	if len(m) != 2 {
		return installerInfo{}, fmt.Errorf("official installer package URL template is not canonical")
	}
	version := string(m[1])
	expected := fmt.Sprintf("https://downloads.cursor.com/lab/%s/%s/%s/agent-cli-package.tar.gz", version, wantOS, wantArch)
	return installerInfo{Version: version, PackageURL: expected}, nil
}

func installerPlatform(goos, goarch string) (string, string, bool) {
	osPart := map[string]string{"linux": "linux", "darwin": "darwin"}[goos]
	archPart := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
	return osPart, archPart, osPart != "" && archPart != ""
}

func (s *Service) RegisterManagement(_ context.Context, _ pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error) {
	return pluginapi.ManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: managementStatusPath, Description: "Reports managed official Cursor Agent CLI installation status.", Handler: s},
			{Method: http.MethodPost, Path: managementInstallPath, Menu: "Install official Cursor Agent CLI", Description: "Explicitly installs the official Cursor Agent CLI into the plugin runtime HOME.", Handler: s},
		},
		Resources: []pluginapi.ResourceRoute{{Path: "/setup", Menu: "Cursor Agent setup", Description: "Explains and confirms official Cursor Agent CLI installation.", Handler: s}},
	}, nil
}

func (s *Service) HandleManagement(ctx context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	path, ok := normalizeCursorManagementPath(req.Path)
	if !ok {
		return jsonResponse(http.StatusNotFound, map[string]any{"error": "unknown Cursor management route"}), nil
	}
	switch {
	case req.Method == http.MethodGet && (path == "/setup" || path == setupResourcePath):
		return htmlResponse(setupHTML()), nil
	case req.Method == http.MethodGet && path == managementStatusPath:
		return jsonResponse(http.StatusOK, s.InstallStatus()), nil
	case req.Method == http.MethodPost && path == managementInstallPath:
		var body struct {
			Confirm bool `json:"confirm"`
		}
		_ = json.Unmarshal(req.Body, &body)
		if !body.Confirm {
			return jsonResponse(http.StatusBadRequest, map[string]any{"installed": false, "error": "explicit confirmation is required"}), nil
		}
		res, err := s.InstallOfficialCursorAgent(ctx)
		if err != nil {
			return jsonResponse(http.StatusBadGateway, map[string]any{"installed": false, "error": err.Error()}), nil
		}
		return jsonResponse(http.StatusOK, res), nil
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"error": "unknown Cursor management route"}), nil
	}
}

func normalizeCursorManagementPath(raw string) (string, bool) {
	path := strings.TrimSpace(raw)
	if path == "" || strings.ContainsAny(path, " 	\r\n") || strings.Contains(path, "..") || strings.Contains(path, "*") || strings.Contains(path, ":") {
		return "", false
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if strings.HasPrefix(path, managementBasePath+"/") {
		path = strings.TrimPrefix(path, managementBasePath)
	}
	if path == setupResourcePath || path == "/setup" || path == managementStatusPath || path == managementInstallPath {
		return path, true
	}
	return "", false
}

func jsonResponse(status int, v any) pluginapi.ManagementResponse {
	b, _ := json.Marshal(v)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return pluginapi.ManagementResponse{StatusCode: status, Headers: h, Body: b}
}
func htmlResponse(html string) pluginapi.ManagementResponse {
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	return pluginapi.ManagementResponse{StatusCode: 200, Headers: h, Body: []byte(html)}
}

func setupHTML() string {
	return `<!doctype html><meta charset="utf-8"><title>Cursor Agent CLI setup</title><main><h1>Install official Cursor Agent CLI</h1><p>This plugin can download the official Cursor Agent CLI package from cursor.com and install it inside the CLIProxy runtime.</p><ul><li>Download source: https://cursor.com/install, parsed to https://downloads.cursor.com/lab/&lt;version&gt;/...</li><li>Install location: runtime HOME/.local/share/cursor-agent/versions/&lt;version&gt;, activated through HOME/.local/share/cursor-agent/current, with HOME/.local/bin/agent and HOME/.local/bin/cursor-agent resolving through that single current pointer.</li><li>The CLI will execute inside the CLIProxy runtime with that runtime's filesystem and network permissions.</li><li>No install happens until you press the explicit confirmation button.</li></ul><label>Management key <input id="k" type="password" autocomplete="off"></label><button id="install">Install official Cursor Agent CLI</button><button id="login">Continue login</button><pre id="out"></pre><script>const out=document.getElementById('out');document.getElementById('install').onclick=async()=>{out.textContent='Installing...';const r=await fetch('/v0/management/plugins/cursor/setup/install',{method:'POST',headers:{'content-type':'application/json','authorization':'Bearer '+document.getElementById('k').value},body:JSON.stringify({confirm:true})});out.textContent=await r.text()};document.getElementById('login').onclick=()=>history.back();</script></main>`
}

func (s *Service) InstallStatus() InstallStatus {
	cfg := s.Config()
	root, err := managedRoot(cfg)
	st := InstallStatus{ManagedRoot: root}
	if err != nil {
		st.Error = err.Error()
		return st
	}
	state, err := inspectManagedInstall(root)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if state.Installed {
		st.Installed = true
		st.Version = state.Version
		st.ExecutablePath = state.ExecutablePath
	}
	return st
}

func (s *Service) InstallOfficialCursorAgent(ctx context.Context) (InstallResult, error) {
	s.installMu.Lock()
	defer s.installMu.Unlock()
	cfg := s.Config()
	root, err := managedRoot(cfg)
	if err != nil {
		return InstallResult{}, err
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("too many redirects")
		}
		if !allowedInstallerHost(req.URL.Hostname()) {
			return fmt.Errorf("redirect to non-Cursor host rejected")
		}
		return nil
	}}
	installerURL := firstNonEmpty(cfg.InstallerURL, officialInstallerURL)
	body, err := fetchBounded(ctx, client, installerURL, 128*1024)
	if err != nil {
		return InstallResult{}, fmt.Errorf("fetch official installer: %w", err)
	}
	info, err := parseOfficialInstaller(body, "linux", "amd64")
	if err != nil {
		return InstallResult{}, err
	}
	if st := s.InstallStatus(); st.Installed && st.Version == info.Version {
		return InstallResult{Installed: true, Version: st.Version, ExecutablePath: st.ExecutablePath}, nil
	}
	pkgURL := info.PackageURL
	if cfg.TestPackageURLOverride != "" {
		pkgURL = cfg.TestPackageURLOverride
	}
	pkg, err := fetchBounded(ctx, client, pkgURL, int64(cfg.MaxPackageBytes))
	if err != nil {
		return InstallResult{}, fmt.Errorf("download Cursor package: %w", err)
	}
	sum := sha256.Sum256(pkg)
	tmp, err := os.MkdirTemp(root, ".cursor-agent-install-*")
	if err != nil {
		return InstallResult{}, fmt.Errorf("create install temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)
	ex, err := safeExtractTarGz(bytes.NewReader(pkg), tmp, extractLimits{MaxEntries: cfg.MaxArchiveEntries, MaxExpandedBytes: int64(cfg.MaxExpandedBytes)})
	if err != nil {
		return InstallResult{}, err
	}
	if err := verifyAgentVersion(ctx, ex.BinaryPath, info.Version); err != nil {
		return InstallResult{}, err
	}
	if _, err := validateExtractedBinaryPath(tmp, ex.ArchiveRelativeBinaryPath); err != nil {
		return InstallResult{}, err
	}
	versionDir := filepath.Join(managedVersionsDir(root), info.Version)
	if err := os.MkdirAll(filepath.Dir(versionDir), 0o755); err != nil {
		return InstallResult{}, err
	}
	prior, err := inspectManagedInstall(root)
	if err != nil {
		return InstallResult{}, err
	}
	if prior.Installed && prior.ArchiveRelativeBinPath != ex.ArchiveRelativeBinaryPath {
		return InstallResult{}, fmt.Errorf("managed Cursor agent binary layout changed from %q to %q; refusing non-transactional migration", prior.ArchiveRelativeBinPath, ex.ArchiveRelativeBinaryPath)
	}
	staged := versionDir + ".new"
	_ = os.RemoveAll(staged)
	if err := installerRename(tmp, staged); err != nil {
		return InstallResult{}, fmt.Errorf("stage Cursor agent: %w", err)
	}
	tmp = ""
	backup := versionDir + ".old"
	_ = os.RemoveAll(backup)
	versionDirExisted := false
	if _, err := os.Stat(versionDir); err == nil {
		versionDirExisted = true
		if err := installerRename(versionDir, backup); err != nil {
			_ = os.RemoveAll(staged)
			return InstallResult{}, fmt.Errorf("backup previous Cursor agent version: %w", err)
		}
	}
	activated := false
	defer func() {
		if !activated {
			if versionDirExisted {
				_ = os.RemoveAll(versionDir)
				_ = installerRename(backup, versionDir)
			} else {
				_ = os.RemoveAll(versionDir)
			}
			if !prior.Installed {
				cleanupInitialInstallLinks(root)
			}
		}
		if activated {
			_ = os.RemoveAll(backup)
		}
	}()
	if err := installerRename(staged, versionDir); err != nil {
		_ = os.RemoveAll(staged)
		return InstallResult{}, fmt.Errorf("activate Cursor agent version dir: %w", err)
	}
	if _, err := validateExtractedBinaryPath(versionDir, ex.ArchiveRelativeBinaryPath); err != nil {
		return InstallResult{}, err
	}
	if prior.Installed {
		if err := replaceCurrentLink(root, prior.Version); err != nil {
			return InstallResult{}, fmt.Errorf("prepare Cursor agent current link for migration: %w", err)
		}
	}
	if err := ensureStableBinLinks(root, ex.ArchiveRelativeBinaryPath); err != nil {
		return InstallResult{}, fmt.Errorf("install Cursor agent bin links: %w", err)
	}
	if err := replaceCurrentLink(root, info.Version); err != nil {
		return InstallResult{}, fmt.Errorf("activate Cursor agent current link: %w", err)
	}
	activated = true
	return InstallResult{Installed: true, Version: info.Version, ExecutablePath: filepath.Join(managedBinDir(root), "agent"), PackageSHA256: hex.EncodeToString(sum[:]), PackageBytes: int64(len(pkg))}, nil
}

func allowedInstallerHost(host string) bool {
	return host == "cursor.com" || host == "www.cursor.com" || host == "downloads.cursor.com" || host == "127.0.0.1" || host == "localhost"
}

func fetchBounded(ctx context.Context, client *http.Client, raw string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	if req.URL.Scheme != "https" && req.URL.Hostname() != "127.0.0.1" && req.URL.Hostname() != "localhost" {
		return nil, fmt.Errorf("installer fetch requires HTTPS")
	}
	if !allowedInstallerHost(req.URL.Hostname()) {
		return nil, fmt.Errorf("non-Cursor host rejected")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("unexpected HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("download exceeds size limit")
	}
	return b, nil
}

func managedRoot(cfg Config) (string, error) {
	root := strings.TrimSpace(cfg.ManagedInstallRoot)
	if root == "" {
		root = os.Getenv("HOME")
	}
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("writable persistent HOME or managed_install_root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("managed root is not writable: %w", err)
	}
	return root, nil
}

func safeExtractTarGz(r io.Reader, dest string, lim extractLimits) (extractedArchive, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return extractedArchive{}, fmt.Errorf("open Cursor package: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	entries := 0
	var total int64
	seen := map[string]struct{}{}
	var bin string
	var binRel string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return extractedArchive{}, err
		}
		entries++
		if lim.MaxEntries > 0 && entries > lim.MaxEntries {
			return extractedArchive{}, fmt.Errorf("archive has too many entries")
		}
		clean, err := safeArchivePath(hdr.Name)
		if err != nil {
			return extractedArchive{}, err
		}
		if _, ok := seen[clean]; ok {
			return extractedArchive{}, fmt.Errorf("duplicate archive path %q", clean)
		}
		seen[clean] = struct{}{}
		target := filepath.Join(dest, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, safeMode(hdr.FileInfo().Mode(), true)); err != nil {
				return extractedArchive{}, err
			}
		case tar.TypeReg:
			total += hdr.Size
			if lim.MaxExpandedBytes > 0 && total > lim.MaxExpandedBytes {
				return extractedArchive{}, fmt.Errorf("archive expanded size exceeds limit")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return extractedArchive{}, err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, safeMode(hdr.FileInfo().Mode(), false))
			if err != nil {
				return extractedArchive{}, err
			}
			_, copyErr := io.CopyN(f, tr, hdr.Size)
			closeErr := f.Close()
			if copyErr != nil && copyErr != io.EOF {
				return extractedArchive{}, copyErr
			}
			if closeErr != nil {
				return extractedArchive{}, closeErr
			}
			if filepath.Base(clean) == "agent" || filepath.Base(clean) == "cursor-agent" {
				bin = target
				binRel = clean
			}
		case tar.TypeSymlink:
			if _, err := safeArchivePath(filepath.Join(filepath.Dir(clean), hdr.Linkname)); err != nil || filepath.IsAbs(hdr.Linkname) || strings.Contains(hdr.Linkname, "..") {
				return extractedArchive{}, fmt.Errorf("unsafe symlink %q", clean)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return extractedArchive{}, err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return extractedArchive{}, err
			}
		default:
			return extractedArchive{}, fmt.Errorf("unsupported archive entry type %d for %q", hdr.Typeflag, clean)
		}
	}
	if bin == "" {
		return extractedArchive{}, fmt.Errorf("cursor package did not contain agent binary")
	}
	return extractedArchive{BinaryPath: bin, ArchiveRelativeBinaryPath: binRel}, nil
}

func validateExtractedBinaryPath(root, archiveRelativePath string) (string, error) {
	clean, err := safeArchivePath(archiveRelativePath)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, clean)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve Cursor agent version dir: %w", err)
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve Cursor agent binary: %w", err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedCandidate)
	if err != nil || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("cursor agent binary escapes version dir")
	}
	return candidate, nil
}

func safeArchivePath(name string) (string, error) {
	clean := filepath.Clean(strings.TrimPrefix(name, "./"))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || strings.Contains(clean, string(filepath.Separator)+".."+string(filepath.Separator)) || strings.Contains(clean, "\x00") {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return clean, nil
}
func safeMode(m os.FileMode, dir bool) os.FileMode {
	if dir {
		return 0o755
	}
	if m&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func verifyAgentVersion(ctx context.Context, bin, version string) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("verify Cursor agent --version: %w", err)
	}
	if !strings.Contains(string(out), version) {
		return fmt.Errorf("cursor agent version mismatch")
	}
	return nil
}
func replaceSymlink(path, target string) error {
	tmp, err := randomSiblingPath(filepath.Dir(path), "."+filepath.Base(path)+"-tmp-")
	if err != nil {
		return err
	}
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := installerRename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func resolveAgentExecutable(cfg Config) (string, error) {
	if strings.Contains(cfg.ExecutablePath, string(filepath.Separator)) || filepath.IsAbs(cfg.ExecutablePath) {
		if _, err := os.Stat(cfg.ExecutablePath); err != nil {
			return "", err
		}
		return cfg.ExecutablePath, nil
	}
	if p, err := exec.LookPath(cfg.ExecutablePath); err == nil {
		return p, nil
	}
	if cfg.ExecutablePath == defaultAgentPath {
		if root, err := managedRoot(cfg); err == nil {
			p := filepath.Join(root, ".local", "bin", "agent")
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("agent executable not found in PATH or managed install")
}
