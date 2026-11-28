package share

import "fmt"

const maxProcessOutput = 256 * 1024

// ProcessOutput retains a bounded tail while draining all process output.
type ProcessOutput struct {
	data      []byte
	start     int // Oldest byte in the full ring; zero until it wraps.
	truncated bool
}

func (b *ProcessOutput) Write(p []byte) (int, error) {
	n := len(p)
	if n == 0 {
		return 0, nil
	}
	if b.data == nil {
		b.data = make([]byte, 0, maxProcessOutput)
	}
	b.truncated = b.truncated || n > maxProcessOutput-len(b.data)
	if n >= maxProcessOutput {
		b.data = append(b.data[:0], p[n-maxProcessOutput:]...)
		b.start = 0
		return n, nil
	}
	fill := min(n, maxProcessOutput-len(b.data))
	b.data = append(b.data, p[:fill]...)
	p = p[fill:]
	if len(p) > 0 {
		first := copy(b.data[b.start:], p)
		copy(b.data, p[first:])
		b.start = (b.start + len(p)) % maxProcessOutput
	}
	return n, nil
}

func (b *ProcessOutput) String() string {
	if !b.truncated {
		return string(b.data)
	}
	return fmt.Sprintf("... output too long; showing last %d KiB ...\n", maxProcessOutput/1024) + string(b.data[b.start:]) + string(b.data[:b.start])
}
