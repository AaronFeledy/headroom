package safestorage

import (
	"bytes"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func Unprotect(protected []byte) (key []byte, err error) {
	if len(protected) == 0 || uint64(len(protected)) > uint64(^uint32(0)) {
		return nil, ErrInvalid
	}
	in := windows.DataBlob{Size: uint32(len(protected)), Data: &protected[0]}
	var out windows.DataBlob
	err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	runtime.KeepAlive(protected)
	if err != nil {
		return nil, ErrUnprotect
	}
	defer func() {
		if _, freeErr := windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))); freeErr != nil {
			clear(key)
			key, err = nil, ErrUnprotect
		}
	}()
	if out.Size != 32 || out.Data == nil {
		return nil, ErrInvalid
	}
	plaintext := unsafe.Slice(out.Data, 32)
	key = bytes.Clone(plaintext)
	clear(plaintext)
	return key, nil
}
