package share

import (
	"fmt"
	"strings"
	"testing"
)

func TestProcessOutputDrainsWithBoundedMemory(t *testing.T) {
	const limit = 256 * 1024
	for _, chunkSize := range []int{0, 1, 4093, limit + 1} {
		t.Run(fmt.Sprint(chunkSize), func(t *testing.T) {
			var output ProcessOutput
			chunk := []byte(strings.Repeat("x", chunkSize))
			total := 0
			for range 300 {
				n, err := output.Write(chunk)
				if err != nil || n != len(chunk) {
					t.Fatalf("write = %d, %v; want %d, nil", n, err, len(chunk))
				}
				total += n
				if len(output.data) != min(total, limit) || cap(output.data) > limit {
					t.Fatalf("retained=%d capacity=%d total=%d", len(output.data), cap(output.data), total)
				}
			}
			want := strings.Repeat("x", min(total, limit))
			if total > limit {
				want += "\n... output too long; truncated to first 256 KiB ...\n"
			}
			if output.String() != want {
				t.Fatal("retained prefix or truncation notice changed")
			}
		})
	}
}
