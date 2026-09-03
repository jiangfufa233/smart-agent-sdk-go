package sse_test

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/example/agent-sdk/model/sse"
)

func decodeAll(t *testing.T, r io.Reader) ([]sse.Event, error) {
	t.Helper()
	d := sse.NewDecoder(r)
	var evs []sse.Event
	for {
		ev, err := d.Next()
		if err == io.EOF {
			return evs, nil
		}
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
}

func TestDecodeBasic(t *testing.T) {
	evs, err := decodeAll(t, strings.NewReader(
		"data: hello\n\n"+
			"event: tool\ndata: {\"i\":1}\n\n"+
			"data: done\n"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []sse.Event{
		{Name: "", Data: "hello"},
		{Name: "tool", Data: `{"i":1}`},
		{Name: "", Data: "done"}, // pending event dispatched at EOF
	}
	if len(evs) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(evs), len(want), evs)
	}
	for i := range want {
		if evs[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, evs[i], want[i])
		}
	}
}

func TestDecodeKeepaliveCommentsAndEmptyEvents(t *testing.T) {
	evs, err := decodeAll(t, strings.NewReader(
		": ping\n\n"+
			"data: a\n"+
			": keepalive\n"+
			"\n"+
			": only-comment-no-data\n\n"+
			"data: b\n\n"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(evs) != 2 || evs[0].Data != "a" || evs[1].Data != "b" {
		t.Fatalf("got %+v, want exactly [a b]", evs)
	}
}

func TestDecodeMultilineData(t *testing.T) {
	evs, err := decodeAll(t, strings.NewReader("data: line1\ndata:line2\ndata: line3\n\n"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(evs) != 1 || evs[0].Data != "line1\nline2\nline3" {
		t.Fatalf("got %+v", evs)
	}
}

func TestDecodeLineEndings(t *testing.T) {
	for name, input := range map[string]string{
		"lf":    "data: a\ndata: b\n\n",
		"crlf":  "data: a\r\ndata: b\r\n\r\n",
		"cr":    "data: a\rdata: b\r\r",
		"mixed": "data: a\r\ndata: b\n\r",
	} {
		evs, err := decodeAll(t, strings.NewReader(input))
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if len(evs) != 1 || evs[0].Data != "a\nb" {
			t.Errorf("%s: got %+v, want one event with data %q", name, evs, "a\nb")
		}
	}
}

func TestDecodeBOM(t *testing.T) {
	evs, err := decodeAll(t, strings.NewReader("\xEF\xBB\xBFdata: a\n\n"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(evs) != 1 || evs[0].Data != "a" {
		t.Fatalf("got %+v", evs)
	}
}

func TestDecodeFieldSemantics(t *testing.T) {
	// "id" is tracked, unknown fields are ignored, a field without a colon
	// has an empty value, only one leading space is stripped, and an id with
	// NUL is ignored.
	evs, err := decodeAll(t, strings.NewReader(
		"id: 7\n"+
			"unknown: ignored\n"+
			"nofield\n"+
			"data:  spaced\n"+
			"\n"+
			"id: bad\x00id\n"+
			"retry: 500\n"+
			"data: second\n\n"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []sse.Event{
		{Name: "", Data: " spaced", LastID: "7"},
		{Name: "", Data: "second", LastID: "7"}, // NUL id ignored
	}
	if len(evs) != len(want) {
		t.Fatalf("got %+v, want %+v", evs, want)
	}
	for i := range want {
		if evs[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, evs[i], want[i])
		}
	}
}

func TestDecodeEventNameOnlyIsDropped(t *testing.T) {
	// Per spec an event with no data is not dispatched, but a later data
	// line must not inherit the earlier event name.
	evs, err := decodeAll(t, strings.NewReader("event: ghost\n\ndata: real\n\n"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(evs) != 1 || evs[0].Name != "" || evs[0].Data != "real" {
		t.Fatalf("got %+v", evs)
	}
}

func TestDecodeEmptyStream(t *testing.T) {
	evs, err := decodeAll(t, strings.NewReader(""))
	if err != nil || len(evs) != 0 {
		t.Fatalf("got %v, %v", evs, err)
	}
	// After EOF, Next keeps returning io.EOF.
	d := sse.NewDecoder(strings.NewReader(""))
	if _, err := d.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("first Next = %v, want io.EOF", err)
	}
	if _, err := d.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("second Next = %v, want io.EOF", err)
	}
}

func TestDecodeMidStreamReadError(t *testing.T) {
	boom := errors.New("connection reset")
	d := sse.NewDecoder(iotest.ErrReader(boom))
	if _, err := d.Next(); !errors.Is(err, boom) {
		t.Fatalf("Next = %v, want %v", err, boom)
	}
}

func TestDecodeLineTooLong(t *testing.T) {
	d := sse.NewDecoder(strings.NewReader("data: " + strings.Repeat("x", 64) + "\n\n"))
	d.SetMaxLineSize(16)
	if _, err := d.Next(); !errors.Is(err, sse.ErrLineTooLong) {
		t.Fatalf("Next = %v, want ErrLineTooLong", err)
	}
}

func TestDecodeChunkedDelivery(t *testing.T) {
	input := "data: a\n\nevent: t\ndata: {\"x\":1}\n\n: c\ndata: done\n"
	want, err := decodeAll(t, strings.NewReader(input))
	if err != nil {
		t.Fatalf("whole: %v", err)
	}
	if len(want) != 3 {
		t.Fatalf("unexpected baseline: %+v", want)
	}

	one, err := decodeAll(t, iotest.OneByteReader(strings.NewReader(input)))
	if err != nil {
		t.Fatalf("one-byte: %v", err)
	}
	if len(one) != len(want) {
		t.Fatalf("one-byte got %d events, want %d", len(one), len(want))
	}
	for i := range want {
		if one[i] != want[i] {
			t.Errorf("one-byte event %d = %+v, want %+v", i, one[i], want[i])
		}
	}
}

func FuzzSSE(f *testing.F) {
	seeds := []string{
		"",
		"data: hello\n\n",
		"data: a\r\ndata: b\r\n\r\n",
		": comment\n\n",
		"event: e\nid: 1\ndata:\n\n",
		"\xEF\xBB\xBFdata: a\n\n",
		"data: [DONE]\n\n",
		"nofield\ndata: x",
		"retry: abc\ndata: x\n\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		whole, wholeErr := decodeAll(t, strings.NewReader(input))

		// Deterministic irregular chunking: chunk sizes 1..7 cycling.
		chunked := &cycleReader{data: input}
		got, gotErr := decodeAll(t, chunked)

		if wholeErr != nil || gotErr != nil {
			if wholeErr == nil || gotErr == nil {
				t.Fatalf("chunking changed error: whole=%v chunked=%v", wholeErr, gotErr)
			}
			return
		}
		if len(whole) != len(got) {
			t.Fatalf("chunking changed event count: %d vs %d", len(whole), len(got))
		}
		for i := range whole {
			if whole[i] != got[i] {
				t.Fatalf("event %d differs: whole=%+v chunked=%+v", i, whole[i], got[i])
			}
		}
	})
}

// cycleReader splits its input into chunks of cycling size 1..7.
type cycleReader struct {
	data  string
	pos   int
	chunk int
}

func (r *cycleReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	r.chunk = r.chunk%7 + 1
	n := r.chunk
	if n > len(p) {
		n = len(p)
	}
	if r.pos+n > len(r.data) {
		n = len(r.data) - r.pos
	}
	copied := copy(p, r.data[r.pos:r.pos+n])
	r.pos += copied
	return copied, nil
}
