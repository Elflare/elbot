package share

import "fmt"

const maxProcessOutput = 256 * 1024

// ProcessOutput retains a bounded prefix while draining all process output.
type ProcessOutput struct {
	data      []byte
	truncated bool
}

func (b *ProcessOutput) Write(p []byte) (int, error) {
	keep := min(len(p), maxProcessOutput-len(b.data))
	if keep > 0 {
		if b.data == nil {
			b.data = make([]byte, 0, maxProcessOutput)
		}
		b.data = append(b.data, p[:keep]...)
	}
	b.truncated = b.truncated || keep < len(p)
	return len(p), nil
}

func (b *ProcessOutput) String() string {
	text := string(b.data)
	if b.truncated {
		text += fmt.Sprintf("\n... output too long; truncated to first %d KiB ...\n", maxProcessOutput/1024)
	}
	return text
}
