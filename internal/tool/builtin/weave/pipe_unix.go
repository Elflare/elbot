//go:build !windows

package weave

import (
	"context"
	"os"
	"os/exec"
)

func openPipes(cmd *exec.Cmd) (*pipes, error) {
	in, responses, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	requests, out, err := os.Pipe()
	if err != nil {
		in.Close()
		responses.Close()
		return nil, err
	}
	cmd.ExtraFiles = []*os.File{in, out}
	return &pipes{
		requests: requests, responses: responses,
		connect:      func(context.Context) error { return nil },
		releaseChild: func() { in.Close(); out.Close() },
		close:        func() { requests.Close(); responses.Close() },
	}, nil
}
