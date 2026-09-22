//go:build darwin || linux

package needle

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestInstalledChecksumRejectsFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := installedChecksum(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("accepted a FIFO")
		}
	case <-time.After(time.Second):
		// Unblock the reader so a regression does not leave a stuck goroutine.
		fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		<-done
		t.Fatal("blocked opening a FIFO before checking its file type")
	}
}
