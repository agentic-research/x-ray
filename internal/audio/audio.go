// Package audio wraps sox's rec and play commands for native audio capture
// and playback. It produces/consumes raw PCM data suitable for streaming to
// and from Google Gemini's Live API.
package audio

import (
	"fmt"
	"io"
	"os/exec"
	"syscall"
)

// Recorder captures audio from the default microphone via sox's rec command.
// Output format: raw PCM, 16-bit signed, mono, 16 kHz (matches Gemini's
// expected input).
type Recorder struct {
	cmd  *exec.Cmd
	pipe io.ReadCloser
}

// NewRecorder creates a Recorder but does not start it.
func NewRecorder() *Recorder {
	cmd := exec.Command(
		"rec",
		"-q",        // quiet – no progress output
		"-t", "raw", // raw PCM output (no headers)
		"-r", "16000", // 16 kHz sample rate
		"-e", "signed", // signed integers
		"-b", "16", // 16-bit samples
		"-c", "1", // mono
		"-", // write to stdout
	)
	return &Recorder{cmd: cmd}
}

// Start begins recording. It returns an io.ReadCloser from which the caller
// can read raw PCM data. The caller should read from this pipe in a
// goroutine; failing to drain the pipe will eventually block the recorder.
func (r *Recorder) Start() (io.ReadCloser, error) {
	pipe, err := r.cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("audio: stdout pipe: %w", err)
	}
	r.pipe = pipe
	if err := r.cmd.Start(); err != nil {
		return nil, fmt.Errorf("audio: start rec: %w", err)
	}
	return pipe, nil
}

// Stop gracefully stops recording by sending SIGTERM to the sox process,
// then waits for it to exit.
func (r *Recorder) Stop() error {
	if r.cmd.Process == nil {
		return nil
	}
	if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("audio: signal rec: %w", err)
	}
	// Wait returns an error for signal-terminated processes; that is expected.
	_ = r.cmd.Wait()
	return nil
}

// Player plays raw PCM audio through the default speaker via sox's play
// command. Input format: raw PCM, 16-bit signed, mono, 24 kHz (matches
// Gemini's audio output).
type Player struct {
	cmd  *exec.Cmd
	pipe io.WriteCloser
}

// NewPlayer creates a Player but does not start it.
func NewPlayer() *Player {
	cmd := exec.Command(
		"play",
		"-q",        // quiet – no progress output
		"-t", "raw", // raw PCM input (no headers)
		"-r", "24000", // 24 kHz sample rate
		"-e", "signed", // signed integers
		"-b", "16", // 16-bit samples
		"-c", "1", // mono
		"-", // read from stdin
	)
	return &Player{cmd: cmd}
}

// Start begins playback. It returns an io.WriteCloser to which the caller
// writes raw PCM data; sox plays it in real-time.
func (p *Player) Start() (io.WriteCloser, error) {
	pipe, err := p.cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("audio: stdin pipe: %w", err)
	}
	p.pipe = pipe
	if err := p.cmd.Start(); err != nil {
		return nil, fmt.Errorf("audio: start play: %w", err)
	}
	return pipe, nil
}

// Stop gracefully stops playback by closing the stdin pipe (signalling EOF
// to sox) and then waiting for it to exit.
func (p *Player) Stop() error {
	if p.pipe != nil {
		if err := p.pipe.Close(); err != nil {
			return fmt.Errorf("audio: close pipe: %w", err)
		}
	}
	if p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Wait(); err != nil {
		return fmt.Errorf("audio: wait play: %w", err)
	}
	return nil
}

// Available reports whether sox (rec and play) is installed and accessible
// on the current system's PATH.
func Available() bool {
	_, errRec := exec.LookPath("rec")
	_, errPlay := exec.LookPath("play")
	return errRec == nil && errPlay == nil
}
