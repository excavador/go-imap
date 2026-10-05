package imapclient_test

import (
	"bufio"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestFetch(t *testing.T) {
	client, server := newClientServerPair(t, imap.ConnStateSelected)
	defer client.Close()
	defer server.Close()

	seqSet := imap.SeqSetNum(1)
	bodySection := &imap.FetchItemBodySection{}
	fetchOptions := &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{bodySection},
	}
	messages, err := client.Fetch(seqSet, fetchOptions).Collect()
	if err != nil {
		t.Fatalf("failed to fetch first message: %v", err)
	} else if len(messages) != 1 {
		t.Fatalf("len(messages) = %v, want 1", len(messages))
	}

	msg := messages[0]
	if len(msg.BodySection) != 1 {
		t.Fatalf("len(msg.BodySection) = %v, want 1", len(msg.BodySection))
	}
	b := msg.FindBodySection(bodySection)
	if b == nil {
		t.Fatalf("FindBodySection() = nil")
	}
	body := strings.ReplaceAll(string(b), "\r\n", "\n")
	if body != simpleRawMessage {
		t.Errorf("body mismatch: got \n%v\n but want \n%v", body, simpleRawMessage)
	}
}

// newScriptedClient returns a client connected to a fake server which answers
// the first command with the given untagged responses followed by a tagged OK.
func newScriptedClient(t *testing.T, respond func(w io.Writer)) *imapclient.Client {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { serverConn.Close() })

	go func() {
		defer serverConn.Close()
		br := bufio.NewReader(serverConn)
		io.WriteString(serverConn, "* PREAUTH [CAPABILITY IMAP4rev1] ready\r\n")
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		tag, _, _ := strings.Cut(line, " ")
		respond(serverConn)
		io.WriteString(serverConn, tag+" OK done\r\n")
		io.Copy(io.Discard, br)
	}()

	client := imapclient.New(clientConn, nil)
	t.Cleanup(func() { client.Close() })
	if err := client.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting() = %v", err)
	}
	return client
}

func TestFetch_skipUnknownAttribute(t *testing.T) {
	values := map[string]string{
		"atom":    "foo",
		"number":  "18446744073709551615",
		"string":  `"hello \"world\""`,
		"nil":     "NIL",
		"list":    `(1 "two" NIL (3 (four)) {5}` + "\r\n" + `abcde ())`,
		"literal": "{12}\r\nhello\r\nworld",
	}
	for name, value := range values {
		t.Run(name, func(t *testing.T) {
			client := newScriptedClient(t, func(w io.Writer) {
				io.WriteString(w, "* 1 FETCH (UID 42 X-UNKNOWN "+value+" FLAGS (\\Seen) BODY[] {5}\r\nhello X-OTHER "+value+")\r\n")
			})

			bodySection := &imap.FetchItemBodySection{}
			msgs, err := client.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{
				UID:         true,
				Flags:       true,
				BodySection: []*imap.FetchItemBodySection{bodySection},
			}).Collect()
			if err != nil {
				t.Fatalf("Fetch().Collect() = %v", err)
			} else if len(msgs) != 1 {
				t.Fatalf("len(msgs) = %v, want 1", len(msgs))
			}

			msg := msgs[0]
			if msg.UID != 42 {
				t.Errorf("UID = %v, want 42", msg.UID)
			}
			if len(msg.Flags) != 1 || msg.Flags[0] != imap.FlagSeen {
				t.Errorf("Flags = %v, want [\\Seen]", msg.Flags)
			}
			if b := msg.FindBodySection(bodySection); string(b) != "hello" {
				t.Errorf("body = %q, want %q", b, "hello")
			}
		})
	}
}

func TestFetch_skipLargeLiteral(t *testing.T) {
	const size = 64 << 20

	client := newScriptedClient(t, func(w io.Writer) {
		io.WriteString(w, "* 1 FETCH (X-UNKNOWN {"+strconv.Itoa(size)+"}\r\n")
		chunk := make([]byte, 32*1024)
		for i := range chunk {
			chunk[i] = 'a'
		}
		for n := 0; n < size; n += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
		io.WriteString(w, " UID 7)\r\n")
	})

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	msgs, err := client.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{UID: true}).Collect()
	if err != nil {
		t.Fatalf("Fetch().Collect() = %v", err)
	} else if len(msgs) != 1 || msgs[0].UID != 7 {
		t.Fatalf("msgs = %v, want one message with UID 7", msgs)
	}

	runtime.ReadMemStats(&after)
	if delta := after.TotalAlloc - before.TotalAlloc; delta > size/8 {
		t.Errorf("skipping a %v byte literal allocated %v bytes", size, delta)
	}
}

func TestFetch_skipUnknownAttributeTooDeep(t *testing.T) {
	depth := 2000
	client := newScriptedClient(t, func(w io.Writer) {
		io.WriteString(w, "* 1 FETCH (X-UNKNOWN "+strings.Repeat("(", depth)+strings.Repeat(")", depth)+")\r\n")
	})

	_, err := client.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{UID: true}).Collect()
	if err == nil {
		t.Fatalf("Fetch().Collect() = nil, want an error")
	} else if !strings.Contains(err.Error(), "depth") {
		t.Errorf("Fetch().Collect() = %q, want an error mentioning depth", err)
	}
}
