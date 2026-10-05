package imapwire_test

import (
	"bufio"
	"errors"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/internal/imapwire"
)

func newTestDecoder(s string) *imapwire.Decoder {
	return imapwire.NewDecoder(bufio.NewReader(strings.NewReader(s)), imapwire.ConnSideServer)
}

func expectDecoderExpectError(t *testing.T, dec *imapwire.Decoder) {
	t.Helper()
	err := dec.Err()
	if err == nil {
		t.Fatal("decoder has no error")
	}
	var decErr *imapwire.DecoderExpectError
	if !errors.As(err, &decErr) {
		t.Fatalf("decoder error is %T (%v), want *DecoderExpectError", err, err)
	}
}

func TestExpectNumSetInvalid(t *testing.T) {
	dec := newTestDecoder("abc\r\n")

	var numSet imap.NumSet
	if dec.ExpectNumSet(imapwire.NumKindSeq, &numSet) {
		t.Fatal("ExpectNumSet accepted an invalid sequence-set")
	}
	expectDecoderExpectError(t, dec)
}

func TestExpectMailboxInvalidUTF7(t *testing.T) {
	dec := newTestDecoder("&\r\n")

	var name string
	if dec.ExpectMailbox(&name) {
		t.Fatal("ExpectMailbox accepted a name that is not modified UTF-7")
	}
	expectDecoderExpectError(t, dec)
}

func TestSkipValue(t *testing.T) {
	for _, s := range []string{
		"foo", "123", `"quoted \"str\""`, "NIL", "{3}\r\nabc", "()",
		`(1 "two" NIL (3 (four)) {3}` + "\r\nabc)",
	} {
		dec := newTestDecoder(s + " tail\r\n")
		if !dec.SkipValue() {
			t.Errorf("SkipValue(%q) failed: %v", s, dec.Err())
			continue
		}
		var tail string
		if !dec.ExpectSP() || !dec.ExpectAtom(&tail) || tail != "tail" {
			t.Errorf("SkipValue(%q) didn't stop at the end of the value: %v", s, dec.Err())
		}
	}
}

func TestSkipValueMaxDepth(t *testing.T) {
	dec := newTestDecoder(strings.Repeat("(", 5000) + "\r\n")
	if dec.SkipValue() {
		t.Fatal("SkipValue accepted a list nested too deeply")
	}
	if err := dec.Err(); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("decoder error = %v, want an error mentioning depth", err)
	}
}
