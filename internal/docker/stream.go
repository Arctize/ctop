package docker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

// Stream identifiers of the multiplexed stdio protocol.
const (
	Stdin  byte = 0
	Stdout byte = 1
	Stderr byte = 2
)

// Demux copies a multiplexed stdout/stderr stream to the writers returned by dst.
func Demux(r io.Reader, dst func(stream byte) io.Writer) error {
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		w := dst(hdr[0])
		if w == nil {
			w = io.Discard
		}
		if _, err := io.CopyN(w, r, n); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// ReadLines reads a log stream and calls fn for each complete line. tty must
// match the container's TTY setting (TTY streams are not multiplexed).
func ReadLines(r io.Reader, tty bool, fn func(stream byte, line string)) error {
	out := &lineWriter{stream: Stdout, fn: fn}
	errw := &lineWriter{stream: Stderr, fn: fn}
	var err error
	if tty {
		_, err = io.Copy(out, r)
	} else {
		err = Demux(r, func(s byte) io.Writer {
			if s == Stderr {
				return errw
			}
			return out
		})
	}
	out.flush()
	errw.flush()
	return err
}

type lineWriter struct {
	stream byte
	buf    []byte
	fn     func(byte, string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.fn(w.stream, string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > 1<<20 { // pathological line without newline
		w.flush()
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.fn(w.stream, string(w.buf))
		w.buf = nil
	}
}
