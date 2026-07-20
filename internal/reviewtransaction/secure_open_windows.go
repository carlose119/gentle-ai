//go:build windows

package reviewtransaction

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func secureOpenLocalStoreLock(path string) (*os.File, error) {
	runSecureOpenLocalStoreLockBeforeOpen(path)

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	objectName, err := windows.NewNTUnicodeString(ntPath(absPath))
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:     uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		ObjectName: objectName,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(
		&handle,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE,
		attributes,
		&status,
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		windows.FILE_OPEN_IF,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT,
		0,
		0,
	)
	if err != nil {
		return nil, err
	}

	fileInfo := new(windows.ByHandleFileInformation)
	if err := windows.GetFileInformationByHandle(handle, fileInfo); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	fileType, err := windows.GetFileType(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if fileType != windows.FILE_TYPE_DISK || fileInfo.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("review store lock %q is not a regular file", path)
	}

	return os.NewFile(uintptr(handle), path), nil
}

func ntPath(path string) string {
	if strings.HasPrefix(path, `\\`) {
		return `\??\UNC\` + strings.TrimPrefix(path, `\\`)
	}
	return `\??\` + path
}
