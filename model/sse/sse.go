// Package sse implements an incremental decoder for the server-sent events
// wire format (text/event-stream) as specified by the WHATWG HTML standard.
//
// The decoder is robust to arbitrary chunking: feeding the same byte stream
// in one call or byte-by-byte yields identical events. Comment lines (used
// as keepalives) are skipped, and a stream that ends mid-event still
// dispatches the pending event once before reporting io.EOF, so short
// responses without a final blank line are not silently dropped.
package sse

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
)

// DefaultMaxLineSize bounds the size of a single line (1 MiB). A line longer
// than the limit makes Next return ErrLineTooLong, protecting against
// runaway or malicious servers.
const DefaultMaxLineSize = 1 << 20

// ErrLineTooLong is returned by Next when a line exceeds the configured
// maximum size.
var ErrLineTooLong = errors.New("sse: line exceeds maximum size")

// Event is one parsed server-sent event.
type Event struct {
	// Name is the "event:" field; empty means the default event type.
	Name string
	// Data is the payload: all "data:" lines joined with "\n".
	Data string
	// LastID is the most recent "id:" field seen up to and including this
	// event, for Last-Event-ID reconnection support.
	LastID string
}

// Decoder incrementally decodes an SSE byte stream. It is not safe for
// concurrent use.
type Decoder struct {
	br      *bufio.Reader
	maxLine int

	pending []string
	name    string
	lastID  string
	bomDone bool
	eof     bool
}

// NewDecoder returns a Decoder reading from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{br: bufio.NewReader(r), maxLine: DefaultMaxLineSize}
}

// SetMaxLineSize overrides the per-line size limit. Zero or negative values
// restore DefaultMaxLineSize.
func (d *Decoder) SetMaxLineSize(n int) {
	if n <= 0 {
		n = DefaultMaxLineSize
	}
	d.maxLine = n
}

// Next returns the next event. It returns io.EOF after a clean end of
// stream; any other non-nil error indicates a broken stream. Once io.EOF is
// reached, subsequent calls keep returning io.EOF.
func (d *Decoder) Next() (Event, error) {
	if d.eof {
		return Event{}, io.EOF
	}
	if !d.bomDone {
		d.bomDone = true
		if b, _ := d.br.Peek(3); len(b) == 3 && bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
			_, _ = d.br.Discard(3)
		}
	}
	for {
		line, err := d.readLine()
		if err == io.EOF {
			d.eof = true
			if ev, ok := d.take(); ok {
				return ev, nil
			}
			return Event{}, io.EOF
		}
		if err != nil {
			return Event{}, err
		}
		if len(line) == 0 {
			if ev, ok := d.take(); ok {
				return ev, nil
			}
			continue
		}
		if line[0] == ':' {
			continue // comment / keepalive
		}
		field, value, _ := strings.Cut(string(line), ":")
		value = strings.TrimPrefix(value, " ") // spec: strip a single leading space
		switch field {
		case "data":
			d.pending = append(d.pending, value)
		case "event":
			d.name = value
		case "id":
			// Spec: an id containing NUL must be ignored.
			if !strings.ContainsRune(value, 0) {
				d.lastID = value
			}
		case "retry":
			// Reconnection hint; this decoder never reconnects, but an
			// unparseable value must not fail the stream.
			_, _ = strconv.Atoi(value)
		}
	}
}

// take dispatches the pending event, if any. An event with no data lines is
// dropped per spec, but its event name buffer is still reset.
func (d *Decoder) take() (Event, bool) {
	if len(d.pending) == 0 {
		d.name = ""
		return Event{}, false
	}
	ev := Event{Name: d.name, Data: strings.Join(d.pending, "\n"), LastID: d.lastID}
	d.pending = d.pending[:0]
	d.name = ""
	return ev, true
}

// readLine returns the next line without its terminator, handling LF, CRLF
// and lone CR. A lone CR must wait for the next byte to know whether it is
// part of a CRLF pair, so the ambiguity is resolved with a one-byte lookahead.
func (d *Decoder) readLine() ([]byte, error) {
	var buf []byte
	for {
		b, err := d.br.ReadByte()
		if err != nil {
			if err == io.EOF && len(buf) > 0 {
				return buf, nil // final line without terminator
			}
			return nil, err
		}
		if b == '\n' {
			return buf, nil
		}
		if b == '\r' {
			if n, nerr := d.br.ReadByte(); nerr == nil && n != '\n' {
				_ = d.br.UnreadByte()
			}
			return buf, nil
		}
		buf = append(buf, b)
		if len(buf) > d.maxLine {
			return nil, ErrLineTooLong
		}
	}
}
