package weave

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openPipes(cmd *exec.Cmd) (*pipes, error) {
	name := `\\.\pipe\elbot-weave-` + rand.Text()
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;OW)")
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	h, err := windows.CreateNamedPipe(path, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_FIRST_PIPE_INSTANCE|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 65536, 65536, 0, &sa)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(h), name)
	cmd.Args = append(cmd.Args, name)
	connect := func(ctx context.Context) error {
		raw, err := file.SyscallConn()
		if err != nil {
			return err
		}
		var connected error
		if err := raw.Control(func(fd uintptr) { connected = awaitClient(ctx, windows.Handle(fd)) }); err != nil {
			return err
		}
		return connected
	}
	return &pipes{requests: file, responses: file, connect: connect, releaseChild: func() {}, close: func() { file.Close() }}, nil
}

func awaitClient(ctx context.Context, h windows.Handle) error {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(event)
	// The low bit keeps this manually awaited operation out of Go's I/O completion port.
	op := windows.Overlapped{HEvent: event | 1}
	err = windows.ConnectNamedPipe(h, &op)
	if err == nil || errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		return nil
	}
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		return err
	}
	canceled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { windows.CancelIoEx(h, &op); close(canceled) })
	var transferred uint32
	err = windows.GetOverlappedResult(h, &op, &transferred, true)
	if !stop() {
		<-canceled
	}
	return err
}
