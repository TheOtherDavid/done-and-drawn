package assets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Local struct {
	root string
}

func New(root string) (*Local, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, err
	}
	return &Local{root: absolute}, nil
}

func (l *Local) Root() string { return l.root }

func (l *Local) Open(relativePath string) (*os.File, error) {
	fullPath, err := l.fullPath(relativePath)
	if err != nil {
		return nil, err
	}
	return os.Open(fullPath)
}

func (l *Local) Save(relativePath string, data []byte) error {
	fullPath, err := l.fullPath(relativePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		return fmt.Errorf("create image directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(fullPath), ".image-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary image: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write image: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync image: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close image: %w", err)
	}
	if err := os.Remove(fullPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("replace previous image: %w", err)
	}
	if err := os.Rename(tempPath, fullPath); err != nil {
		return fmt.Errorf("publish image: %w", err)
	}
	return nil
}

func (l *Local) Delete(relativePath string) error {
	fullPath, err := l.fullPath(relativePath)
	if err != nil {
		return err
	}
	if err := os.Remove(fullPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (l *Local) fullPath(relativePath string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relativePath))
	if relativePath == "" || filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("invalid image path")
	}
	return filepath.Join(l.root, clean), nil
}
