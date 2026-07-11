package repository

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func putFile(path string, data []byte) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create object directory: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect object path: %w", err)
	}

	temp, err := os.CreateTemp(filepath.Dir(path), ".object-*.tmp")
	if err != nil {
		return false, fmt.Errorf("create temporary object: %w", err)
	}
	tempName := temp.Name()
	defer func() {
		_ = os.Remove(tempName)
	}()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return false, fmt.Errorf("protect temporary object: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return false, fmt.Errorf("write temporary object: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return false, fmt.Errorf("sync temporary object: %w", err)
	}
	if err := temp.Close(); err != nil {
		return false, fmt.Errorf("close temporary object: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("commit object: %w", err)
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		syncErr := fmt.Errorf("sync object directory: %w", err)
		removeErr := os.Remove(path)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			removeErr = fmt.Errorf("remove unsynced object: %w", removeErr)
		}
		resyncErr := syncDir(filepath.Dir(path))
		if resyncErr != nil {
			resyncErr = fmt.Errorf("resync object directory: %w", resyncErr)
		}
		return false, errors.Join(syncErr, removeErr, resyncErr)
	}
	return true, nil
}

func createSyncedFile(path string, data []byte, mode os.FileMode) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}

	closed := false
	committed := false
	defer func() {
		if !closed {
			err = errors.Join(err, file.Close())
		}
		if committed {
			return
		}

		removeErr := os.Remove(path)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove incomplete file: %w", removeErr))
			return
		}
		if removeErr == nil {
			err = errors.Join(err, syncDir(filepath.Dir(path)))
		}
	}()

	written, err := file.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		return err
	}
	closed = true
	if err := file.Close(); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return err
	}
	committed = true
	return nil
}

func readFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}

	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("object exceeds %d bytes", limit)
	}
	return data, nil
}
