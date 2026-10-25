package orch

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// userError is a store or validation error; its text is part of the CLI's
// contract.
type userError struct{ msg string }

func (e *userError) Error() string { return e.msg }

func userErrorf(format string, args ...any) error {
	return &userError{msg: fmt.Sprintf(format, args...)}
}

// notFoundError exits 2. With output set, the CLI prints that result on
// stdout instead of an error line.
type notFoundError struct {
	msg    string
	output *notFoundOutput
}

type notFoundOutput struct {
	compact string
	json    any
}

func (e *notFoundError) Error() string { return e.msg }

// validUTF8 replaces each run of invalid bytes with U+FFFD, as the
// TypeScript CLI's text decoding did for arguments, files, and tool output.
func validUTF8(s string) string { return strings.ToValidUTF8(s, "\uFFFD") }

func readFileText(path string) (string, error) {
	data, err := os.ReadFile(path)
	return validUTF8(string(data)), err
}

func randomUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func createExclusive(path, contents string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(contents); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// atomicWrite writes a uniquely named sibling and renames it into place.
func atomicWrite(path, contents string) error {
	temporary := filepath.Join(filepath.Dir(path),
		"."+filepath.Base(path)+"."+strconv.Itoa(os.Getpid())+"."+randomUUID()+".tmp")
	err := createExclusive(temporary, contents)
	if err == nil {
		err = os.Rename(temporary, path)
	}
	if removeErr := os.Remove(temporary); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
		return removeErr
	}
	return err
}

func writeIfMissing(path, contents string) error {
	found, err := exists(path)
	if err != nil || found {
		return err
	}
	return atomicWrite(path, contents)
}

// resolvePath makes path absolute against getcwd rather than $PWD, so a
// symlinked working directory resolves to the same store path as before.
func resolvePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	cwd, err := syscall.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, path), nil
}
