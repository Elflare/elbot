//go:build linux

package remote

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestFrontendRejectsFIFOWithoutBlocking(t *testing.T) {
	for _, entry := range []bool{true, false} {
		t.Run(map[bool]string{true: "entry", false: "request"}[entry], func(t *testing.T) {
			dir := testFrontendDir(t)
			name := "pipe"
			if entry {
				name = "index.html"
				if err := os.Remove(filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			fifo := filepath.Join(dir, name)
			if err := syscall.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				// 若回归导致只读打开阻塞，解除阻塞以便测试正常收尾。
				writer, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
				if err == nil {
					writer.Close()
				}
			})
			done := make(chan struct{})
			go func() {
				defer close(done)
				handler, err := newFrontendHandler(dir)
				if entry {
					if err == nil {
						t.Error("accepted FIFO entry")
					}
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/pipe", nil))
				if response.Code != http.StatusNotFound {
					t.Errorf("FIFO request status=%d", response.Code)
				}
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("frontend blocked opening FIFO")
			}
		})
	}
}
