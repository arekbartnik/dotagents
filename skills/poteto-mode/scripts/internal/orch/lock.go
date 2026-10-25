package orch

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const lockFile = ".orch.lock"

func readHolder(path string) (string, error) {
	raw, err := readFileText(path)
	if err != nil {
		return "", err
	}
	if holder := strings.TrimSpace(raw); holder != "" {
		return holder, nil
	}
	return "unknown", nil
}

// holderIsDead reports whether holder is a canonical pid with no process.
// Pids beyond int32 count as alive, since process.kill rejected them.
func holderIsDead(holder string) bool {
	pid, err := strconv.ParseInt(holder, 10, 64)
	if err != nil || pid <= 0 || pid > math.MaxInt32 || strconv.FormatInt(pid, 10) != holder {
		return false
	}
	return errors.Is(syscall.Kill(int(pid), 0), syscall.ESRCH)
}

// acquireLock takes the store's pid lock and returns its release function.
func acquireLock(dir string, options storeOptions) (func() error, error) {
	path := filepath.Join(dir, lockFile)
	pid := strconv.Itoa(os.Getpid())
	create := func() error { return createExclusive(path, pid+"\n") }

	takeOver := func() error {
		if err := os.Remove(path); err != nil {
			return err
		}
		err := create()
		if errors.Is(err, fs.ErrExist) {
			holder, readErr := readHolder(path)
			if readErr != nil {
				return readErr
			}
			return userErrorf("store lock held by pid %s", holder)
		}
		return err
	}

	if err := create(); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		holder, readErr := readHolder(path)
		if readErr != nil {
			holder = "unknown"
		}
		switch {
		case holderIsDead(holder):
			if options.onStaleLock != nil {
				options.onStaleLock(holder)
			}
			err = takeOver()
		case options.force:
			if options.onLockStolen != nil {
				options.onLockStolen(holder)
			}
			err = takeOver()
		default:
			err = userErrorf("store lock held by pid %s", holder)
		}
		if err != nil {
			return nil, err
		}
	}

	return func() error {
		raw, err := readFileText(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(raw) == pid {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return nil
	}, nil
}
