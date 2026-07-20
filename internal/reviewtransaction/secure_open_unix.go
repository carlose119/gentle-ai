//go:build unix

package reviewtransaction

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func secureOpenLocalStoreLock(path string) (*os.File, error) {
	runSecureOpenLocalStoreLockBeforeOpen(path)

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	parentFD, err := secureOpenLockParent(filepath.Dir(absPath))
	if err != nil {
		return nil, err
	}
	defer unix.Close(parentFD)

	fd, err := unix.Openat(parentFD, filepath.Base(absPath), unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("review store lock %q is not a regular file", path)
	}

	return os.NewFile(uintptr(fd), path), nil
}

func secureOpenLockParent(path string) (int, error) {
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		nextFD, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			_ = unix.Close(fd)
			return -1, err
		}
		_ = unix.Close(fd)
		fd = nextFD
	}
	return fd, nil
}
