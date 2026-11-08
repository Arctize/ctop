package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/Arctize/ctop/internal/docker"
)

// shellCmd prefers bash, then ash, then sh.
var shellCmd = []string{"/bin/sh", "-c",
	`if command -v bash >/dev/null 2>&1; then exec bash; elif command -v ash >/dev/null 2>&1; then exec ash; else exec sh; fi`}

// session is a tea.ExecCommand that runs an interactive exec/attach against
// the Docker API while Bubble Tea has released the terminal.
type session struct {
	cli    *docker.Client
	id     string
	name   string
	attach bool

	stdin          io.Reader
	stdout, stderr io.Writer
	result         string
}

func (s *session) SetStdin(r io.Reader)  { s.stdin = r }
func (s *session) SetStdout(w io.Writer) { s.stdout = w }
func (s *session) SetStderr(w io.Writer) { s.stderr = w }

func (m *Model) runSession(c *docker.Container, attach bool) tea.Cmd {
	s := &session{cli: m.cli, id: c.ID, name: c.Name(), attach: attach}
	return tea.Exec(s, func(err error) tea.Msg { return execDoneMsg{text: s.result, err: err} })
}

// tailBuffer remembers the last few KB written to it.
type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tailBuffer) lastLine() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := strings.TrimSpace(string(bytes.ReplaceAll(t.b, []byte("\r"), nil)))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return ansiStripSimple(s)
}

func ansiStripSimple(s string) string {
	return reCSI.ReplaceAllString(reOSC.ReplaceAllString(s, ""), "")
}

func termSize(w io.Writer) (uint, uint) {
	if f, ok := w.(*os.File); ok {
		if cols, rows, err := term.GetSize(int(f.Fd())); err == nil {
			return uint(rows), uint(cols)
		}
	}
	if cols, rows, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		return uint(rows), uint(cols)
	}
	return 24, 80
}

func (s *session) Run() error {
	if s.stdin == nil {
		s.stdin = os.Stdin
	}
	if s.stdout == nil {
		s.stdout = os.Stdout
	}
	if s.stderr == nil {
		s.stderr = os.Stderr
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, w := termSize(s.stdout)
	tty := true
	var conn *docker.HijackedConn
	var execID string
	var err error
	var resize func(h, w uint)

	if s.attach {
		cj, _, err := s.cli.ContainerInspect(ctx, s.id)
		if err != nil {
			return err
		}
		if !cj.State.Running {
			return fmt.Errorf("%s is not running", s.name)
		}
		tty = cj.Config.Tty
		fmt.Fprintf(s.stdout, "\r\n\x1b[2mctop: attached to %s — detach with ctrl-p ctrl-q\x1b[0m\r\n", s.name)
		if !cj.Config.OpenStdin {
			fmt.Fprintf(s.stdout, "\x1b[2m(container has no stdin; output only — ctrl-c returns)\x1b[0m\r\n")
		}
		conn, err = s.cli.ContainerAttach(ctx, s.id)
		if err != nil {
			return err
		}
		resize = func(h, w uint) { _ = s.cli.ContainerResize(context.Background(), s.id, h, w) }
	} else {
		termEnv := os.Getenv("TERM")
		if termEnv == "" {
			termEnv = "xterm-256color"
		}
		execID, err = s.cli.ExecCreate(ctx, s.id, shellCmd, []string{"TERM=" + termEnv}, true)
		if err != nil {
			return err
		}
		fmt.Fprintf(s.stdout, "\r\n\x1b[2mctop: shell in %s — exit to return\x1b[0m\r\n", s.name)
		conn, err = s.cli.ExecStart(ctx, execID, true, h, w)
		if err != nil {
			return err
		}
		resize = func(h, w uint) { _ = s.cli.ExecResize(context.Background(), execID, h, w) }
	}
	defer conn.Close()

	// Raw mode so keys (ctrl-c, arrows, …) reach the container.
	var restore func()
	inFile, isFile := s.stdin.(*os.File)
	if tty && isFile && term.IsTerminal(int(inFile.Fd())) {
		if st, err := term.MakeRaw(int(inFile.Fd())); err == nil {
			restore = func() { _ = term.Restore(int(inFile.Fd()), st) }
			defer restore()
		}
	}

	if tty {
		if s.attach {
			// nudge the process to redraw
			resize(h+1, w+1)
		}
		resize(h, w)
	}

	if !tty {
		// cooked mode: ctrl-c would kill ctop, so use it to end the session
		intr := make(chan os.Signal, 1)
		signal.Notify(intr, os.Interrupt)
		defer signal.Stop(intr)
		go func() {
			if _, ok := <-intr; ok {
				conn.Close()
			}
		}()
	}

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			if tty {
				h, w := termSize(s.stdout)
				resize(h, w)
			}
		}
	}()

	// stdin → container. A cancelable reader is required so the goroutine
	// doesn't keep stealing keystrokes from Bubble Tea after we return.
	cr, err := cancelreader.NewReader(s.stdin)
	if err != nil {
		return err
	}
	inDone := make(chan struct{})
	go func() {
		defer close(inDone)
		_, _ = io.Copy(conn, cr)
		if !tty {
			_ = conn.CloseWrite()
		}
	}()

	// container → terminal
	tail := &tailBuffer{}
	start := time.Now()
	if tty {
		_, _ = io.Copy(io.MultiWriter(s.stdout, tail), conn)
	} else {
		_ = docker.Demux(conn, func(st byte) io.Writer {
			if st == docker.Stderr {
				return io.MultiWriter(s.stderr, tail)
			}
			return io.MultiWriter(s.stdout, tail)
		})
	}
	cr.Cancel()
	conn.Close()
	select {
	case <-inDone:
	case <-time.After(time.Second):
	}
	cr.Close()
	if restore != nil {
		restore()
		restore = nil
	}

	if s.attach {
		s.result = "detached from " + s.name
		return nil
	}
	code, _, err := s.cli.ExecExitCode(context.Background(), execID)
	if err == nil && code != 0 {
		last := tail.lastLine()
		if time.Since(start) < 3*time.Second && last != "" {
			return fmt.Errorf("shell in %s failed (exit %d): %s", s.name, code, last)
		}
		s.result = fmt.Sprintf("shell in %s exited with code %d", s.name, code)
		return nil
	}
	s.result = "shell in " + s.name + " closed"
	return nil
}
