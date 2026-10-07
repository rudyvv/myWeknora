//go:build windows

package main

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"unsafe"
)

const (
	th32csSnapProcess              = 0x00000002
	processQueryLimitedInformation = 0x00001000
	maxProcessImagePathBuffer      = 32768
)

var queryFullProcessImageNameW = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")

func enumeratePinnedBackendImages(ctx context.Context, targetBaseName string) ([]string, error) {
	snapshot, err := syscall.CreateToolhelp32Snapshot(th32csSnapProcess, 0)
	if err != nil {
		return nil, errors.New("process snapshot could not be created")
	}
	defer syscall.CloseHandle(snapshot)
	if err := queryFullProcessImageNameW.Find(); err != nil {
		return nil, errors.New("process image path API is unavailable")
	}

	entry := syscall.ProcessEntry32{Size: uint32(unsafe.Sizeof(syscall.ProcessEntry32{}))}
	if err := syscall.Process32First(snapshot, &entry); err != nil {
		return nil, errors.New("process snapshot could not be read")
	}
	images := make([]string, 0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		imageBase := syscall.UTF16ToString(entry.ExeFile[:])
		if imageBase == "" {
			return nil, errors.New("process executable name is unavailable")
		}
		if strings.EqualFold(imageBase, targetBaseName) {
			process, err := syscall.OpenProcess(processQueryLimitedInformation, false, entry.ProcessID)
			if err != nil {
				return nil, errors.New("candidate process could not be opened")
			}
			imagePath, pathErr := queryProcessImagePath(process)
			_ = syscall.CloseHandle(process)
			if pathErr != nil {
				return nil, errors.New("candidate process image path is unavailable")
			}
			images = append(images, imagePath)
		}

		entry.Size = uint32(unsafe.Sizeof(syscall.ProcessEntry32{}))
		if err := syscall.Process32Next(snapshot, &entry); err != nil {
			if err == syscall.ERROR_NO_MORE_FILES {
				break
			}
			return nil, errors.New("process snapshot enumeration ended unexpectedly")
		}
	}
	return images, nil
}

func queryProcessImagePath(process syscall.Handle) (string, error) {
	buffer := make([]uint16, maxProcessImagePathBuffer)
	length := uint32(len(buffer))
	result, _, callErr := queryFullProcessImageNameW.Call(
		uintptr(process),
		0,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(unsafe.Pointer(&length)),
	)
	if result == 0 || length == 0 || int(length) > len(buffer) {
		if callErr != syscall.Errno(0) {
			return "", callErr
		}
		return "", errors.New("process image path query failed")
	}
	return syscall.UTF16ToString(buffer[:length]), nil
}
