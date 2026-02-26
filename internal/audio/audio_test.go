package audio

import (
	"os/exec"
	"testing"
	"time"
)

func TestRecorderCreation(t *testing.T) {
	r := NewRecorder()
	if r == nil {
		t.Fatal("NewRecorder returned nil")
	}
	if r.cmd == nil {
		t.Fatal("NewRecorder cmd is nil")
	}

	args := r.cmd.Args
	// args[0] is the path to rec (or "rec" itself).
	if len(args) < 1 {
		t.Fatal("expected at least one arg")
	}

	// Verify key flags are present.
	want := []string{"-q", "-t", "raw", "-r", "16000", "-e", "signed", "-b", "16", "-c", "1", "-"}
	got := args[1:] // skip the executable name
	if len(got) != len(want) {
		t.Fatalf("arg count mismatch: got %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i+1, got[i], want[i])
		}
	}
}

func TestPlayerCreation(t *testing.T) {
	p := NewPlayer()
	if p == nil {
		t.Fatal("NewPlayer returned nil")
	}
	if p.cmd == nil {
		t.Fatal("NewPlayer cmd is nil")
	}

	args := p.cmd.Args
	if len(args) < 1 {
		t.Fatal("expected at least one arg")
	}

	want := []string{"-q", "-t", "raw", "-r", "24000", "-e", "signed", "-b", "16", "-c", "1", "-"}
	got := args[1:]
	if len(got) != len(want) {
		t.Fatalf("arg count mismatch: got %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i+1, got[i], want[i])
		}
	}
}

func TestRecorderStartStop(t *testing.T) {
	if _, err := exec.LookPath("rec"); err != nil {
		t.Skip("sox not installed")
	}

	r := NewRecorder()
	pipe, err := r.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Let it record for a brief moment.
	time.Sleep(100 * time.Millisecond)

	// Read whatever bytes are available.
	buf := make([]byte, 4096)
	n, readErr := pipe.Read(buf)
	// A short read or EOF after stop is acceptable; we only care that
	// the process ran successfully.
	if n == 0 && readErr != nil {
		t.Logf("read returned 0 bytes (err=%v); mic may not be available in CI", readErr)
	} else {
		t.Logf("read %d bytes from recorder", n)
	}

	if err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestPlayerStartStop(t *testing.T) {
	if _, err := exec.LookPath("play"); err != nil {
		t.Skip("sox not installed")
	}

	p := NewPlayer()
	pipe, err := p.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Write a small silence buffer (zeros = silence in PCM).
	silence := make([]byte, 4800) // 100ms of 24kHz 16-bit mono = 4800 bytes
	if _, err := pipe.Write(silence); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
