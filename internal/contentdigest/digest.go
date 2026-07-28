package contentdigest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// File returns the SHA-256 digest of a regular, non-symlink file.
func File(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("path is not a regular non-symlink file")
	}
	handle, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, handle); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// Directory returns a deterministic digest of a real directory tree. Portable
// directory sources reject symlinks and special files so their contents cannot
// escape the configuration repository.
func Directory(path string) (string, error) {
	rootInfo, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("path is not a real directory")
	}
	var paths []string
	err = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == path {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 ||
			(!info.IsDir() && !info.Mode().IsRegular()) {
			relative, _ := filepath.Rel(path, current)
			return fmt.Errorf("unsupported entry %q", filepath.ToSlash(relative))
		}
		paths = append(paths, current)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	hasher := sha256.New()
	for _, current := range paths {
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 ||
			(!info.IsDir() && !info.Mode().IsRegular()) {
			relative, _ := filepath.Rel(path, current)
			return "", fmt.Errorf("unsupported entry %q", filepath.ToSlash(relative))
		}
		relative, err := filepath.Rel(path, current)
		if err != nil {
			return "", err
		}
		entryType := byte('f')
		if info.IsDir() {
			entryType = 'd'
		}
		hasher.Write([]byte{entryType, 0})
		hasher.Write([]byte(filepath.ToSlash(relative)))
		hasher.Write([]byte{0})
		if info.Mode().IsRegular() {
			handle, err := os.Open(current)
			if err != nil {
				return "", err
			}
			_, copyErr := io.Copy(hasher, handle)
			closeErr := handle.Close()
			if copyErr != nil {
				return "", copyErr
			}
			if closeErr != nil {
				return "", closeErr
			}
			hasher.Write([]byte{0})
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
