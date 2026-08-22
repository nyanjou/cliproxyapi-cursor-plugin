package provider

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var installerRename = os.Rename

type managedInstallState struct {
	Installed              bool
	Version                string
	ExecutablePath         string
	ArchiveRelativeBinPath string
	ResolvedExecutablePath string
}

func managedShareDir(root string) string {
	return filepath.Join(root, ".local", "share", "cursor-agent")
}

func managedVersionsDir(root string) string {
	return filepath.Join(managedShareDir(root), "versions")
}

func managedCurrentLink(root string) string {
	return filepath.Join(managedShareDir(root), "current")
}

func managedBinDir(root string) string {
	return filepath.Join(root, ".local", "bin")
}

func inspectManagedInstall(root string) (managedInstallState, error) {
	binDir := managedBinDir(root)
	links := []string{filepath.Join(binDir, "agent"), filepath.Join(binDir, "cursor-agent")}
	resolved := make([]string, 0, len(links))
	missing := 0
	for _, link := range links {
		p, err := filepath.EvalSymlinks(link)
		if err != nil {
			if os.IsNotExist(err) {
				missing++
				continue
			}
			return managedInstallState{}, fmt.Errorf("resolve managed Cursor agent link %s: %w", filepath.Base(link), err)
		}
		resolved = append(resolved, p)
	}
	if missing == len(links) {
		return managedInstallState{}, nil
	}
	if missing > 0 || len(resolved) != len(links) {
		return managedInstallState{}, fmt.Errorf("managed Cursor agent links are incomplete")
	}
	versionsRoot := managedVersionsDir(root)
	state := managedInstallState{Installed: true, ExecutablePath: links[0], ResolvedExecutablePath: resolved[0]}
	for i, p := range resolved {
		version, archiveRel, err := managedVersionAndArchiveRel(versionsRoot, p)
		if err != nil {
			return managedInstallState{}, err
		}
		if i == 0 {
			state.Version = version
			state.ArchiveRelativeBinPath = archiveRel
			continue
		}
		if version != state.Version || archiveRel != state.ArchiveRelativeBinPath {
			return managedInstallState{}, fmt.Errorf("managed Cursor agent links are mixed")
		}
	}
	return state, nil
}

func managedVersionAndArchiveRel(versionsRoot, resolved string) (string, string, error) {
	versionsRootAbs, err := filepath.EvalSymlinks(versionsRoot)
	if err != nil {
		versionsRootAbs, err = filepath.Abs(versionsRoot)
		if err != nil {
			return "", "", err
		}
	}
	resolvedAbs, err := filepath.Abs(resolved)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(versionsRootAbs, resolvedAbs)
	if err != nil || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("managed Cursor agent link escapes managed versions")
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 || parts[0] == "" {
		return "", "", fmt.Errorf("managed Cursor agent link does not include a version")
	}
	return parts[0], filepath.FromSlash(strings.Join(parts[1:], "/")), nil
}

func replaceCurrentLink(root, version string) error {
	share := managedShareDir(root)
	if err := os.MkdirAll(share, 0o755); err != nil {
		return err
	}
	current := managedCurrentLink(root)
	if pointsToManagedVersion(root, current, version) {
		return nil
	}
	target := filepath.Join("versions", version)
	if err := replaceSymlink(current, target); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return fmt.Errorf("resolve Cursor agent current link: %w", err)
	}
	versionsRoot, err := filepath.EvalSymlinks(managedVersionsDir(root))
	if err != nil {
		return fmt.Errorf("resolve Cursor agent versions dir: %w", err)
	}
	rel, err := filepath.Rel(versionsRoot, resolved)
	if err != nil || rel != version {
		return fmt.Errorf("cursor agent current link escapes managed versions")
	}
	return nil
}

func pointsToManagedVersion(root, current, version string) bool {
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return false
	}
	want, err := filepath.Abs(filepath.Join(managedVersionsDir(root), version))
	if err != nil {
		return false
	}
	got, err := filepath.Abs(resolved)
	return err == nil && got == want
}

func ensureStableBinLinks(root, archiveRel string) error {
	binDir := managedBinDir(root)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	target, err := filepath.Rel(binDir, filepath.Join(managedCurrentLink(root), archiveRel))
	if err != nil {
		return err
	}
	if filepath.IsAbs(target) {
		return fmt.Errorf("managed Cursor agent bin link target must be relative")
	}
	for _, name := range []string{"agent", "cursor-agent"} {
		if err := replaceSymlink(filepath.Join(binDir, name), target); err != nil {
			return err
		}
	}
	return nil
}

func cleanupInitialInstallLinks(root string) {
	for _, name := range []string{"agent", "cursor-agent"} {
		link := filepath.Join(managedBinDir(root), name)
		if target, err := os.Readlink(link); err == nil && strings.Contains(filepath.ToSlash(target), "/current/") {
			_ = os.Remove(link)
		}
	}
	_ = os.Remove(managedCurrentLink(root))
}

func randomSiblingPath(dir, prefix string) (string, error) {
	for i := 0; i < 16; i++ {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		p := filepath.Join(dir, prefix+hex.EncodeToString(b[:]))
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			return p, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("could not allocate unique temp path")
}
