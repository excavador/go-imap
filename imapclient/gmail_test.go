package imapclient_test

import (
	"bufio"
	"io"
	"net"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// scriptedServer is a minimal fake IMAP server: it reads one command at a time
// (following synchronizing literals), records it and answers with the lines
// returned by reply.
type scriptedServer struct {
	cmds chan string
}

// newScriptedGmailClient returns a client connected to a scripted server which
// advertises X-GM-EXT-1. reply receives the tag and the command with literals
// inlined as {n}<data>, and returns the untagged response lines to send before
// the tagged OK.
func newScriptedGmailClient(t *testing.T, reply func(tag, cmd string) string) (*imapclient.Client, *scriptedServer) {
	clientConn, serverConn := net.Pipe()
	srv := &scriptedServer{cmds: make(chan string, 16)}

	go func() {
		defer serverConn.Close()
		br := bufio.NewReader(serverConn)
		io.WriteString(serverConn, "* OK [CAPABILITY IMAP4rev1 X-GM-EXT-1] ready\r\n")
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimSuffix(line, "\r\n")
			for strings.HasSuffix(cmd, "}") {
				i := strings.LastIndex(cmd, "{")
				n, err := strconv.Atoi(cmd[i+1 : len(cmd)-1])
				if err != nil {
					return
				}
				io.WriteString(serverConn, "+ go ahead\r\n")
				data := make([]byte, n)
				if _, err := io.ReadFull(br, data); err != nil {
					return
				}
				rest, err := br.ReadString('\n')
				if err != nil {
					return
				}
				cmd = cmd[:i] + "{" + strconv.Itoa(n) + "}" + string(data) + strings.TrimSuffix(rest, "\r\n")
			}
			tag, cmd, _ := strings.Cut(cmd, " ")
			srv.cmds <- cmd
			io.WriteString(serverConn, reply(tag, cmd))
			io.WriteString(serverConn, tag+" OK done\r\n")
		}
	}()

	client := imapclient.New(clientConn, nil)
	t.Cleanup(func() { client.Close() })
	if err := client.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting() = %v", err)
	}
	return client, srv
}

func (srv *scriptedServer) next(t *testing.T) string {
	t.Helper()
	select {
	case cmd := <-srv.cmds:
		return cmd
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for command")
		return ""
	}
}

func TestFetch_gmail(t *testing.T) {
	client, srv := newScriptedGmailClient(t, func(tag, cmd string) string {
		return "* 1 FETCH (X-GM-LABELS (\"My Label\" \\Inbox \\Important &BB4EOwQ1BDM- \"a\\\"b\" Work) " +
			"X-GM-THRID 1278455344230334866 UID 42 FLAGS (\\Seen) " +
			"X-GM-MSGID 1278455344230334865 BODY[] {5}\r\nhello)\r\n" +
			"* 2 FETCH (UID 43 X-GM-LABELS () X-GM-MSGID 18446744073709551615 X-GM-THRID 7)\r\n"
	})

	options := &imap.FetchOptions{
		UID:           true,
		Flags:         true,
		GmailMsgID:    true,
		GmailThreadID: true,
		GmailLabels:   true,
		BodySection:   []*imap.FetchItemBodySection{{Peek: true}},
	}
	msgs, err := client.Fetch(imap.SeqSetNum(1, 2), options).Collect()
	if err != nil {
		t.Fatalf("Fetch().Collect() = %v", err)
	}

	cmd := srv.next(t)
	for _, item := range []string{"UID", "FLAGS", "X-GM-MSGID", "X-GM-THRID", "X-GM-LABELS", "BODY.PEEK[]"} {
		if !strings.Contains(cmd, item) {
			t.Errorf("command %q is missing %v", cmd, item)
		}
	}

	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %v, want 2", len(msgs))
	}

	m := msgs[0]
	if m.UID != 42 || m.GmailMsgID != 1278455344230334865 || m.GmailThreadID != 1278455344230334866 {
		t.Errorf("msg 1 = UID %v, MsgID %v, ThreadID %v", m.UID, m.GmailMsgID, m.GmailThreadID)
	}
	wantLabels := []string{"My Label", `\Inbox`, `\Important`, "Олег", `a"b`, "Work"}
	if !reflect.DeepEqual(m.GmailLabels, wantLabels) {
		t.Errorf("msg 1 labels = %q, want %q", m.GmailLabels, wantLabels)
	}
	if !reflect.DeepEqual(m.Flags, []imap.Flag{imap.FlagSeen}) {
		t.Errorf("msg 1 flags = %v", m.Flags)
	}
	if got := string(m.FindBodySection(&imap.FetchItemBodySection{})); got != "hello" {
		t.Errorf("msg 1 body = %q, want hello", got)
	}

	m = msgs[1]
	if m.UID != 43 || m.GmailMsgID != 18446744073709551615 || m.GmailThreadID != 7 {
		t.Errorf("msg 2 = UID %v, MsgID %v, ThreadID %v", m.UID, m.GmailMsgID, m.GmailThreadID)
	}
	if m.GmailLabels == nil || len(m.GmailLabels) != 0 {
		t.Errorf("msg 2 labels = %#v, want empty non-nil list", m.GmailLabels)
	}
}

func TestFetch_unknownAttributeSkipped(t *testing.T) {
	client, _ := newScriptedGmailClient(t, func(tag, cmd string) string {
		return "* 1 FETCH (X-FOO-ATOM bar X-FOO-NUM 12 X-FOO-STR \"a b\" " +
			"X-FOO-LIST (1 (nested \"x\" NIL) {3}\r\nabc) UID 9 X-GM-MSGID 5 X-FOO-LIT {4}\r\nwxyz FLAGS (\\Seen))\r\n"
	})

	msgs, err := client.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{UID: true, Flags: true, GmailMsgID: true}).Collect()
	if err != nil {
		t.Fatalf("Fetch().Collect() = %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %v, want 1", len(msgs))
	}
	m := msgs[0]
	if m.UID != 9 || m.GmailMsgID != 5 || !reflect.DeepEqual(m.Flags, []imap.Flag{imap.FlagSeen}) {
		t.Errorf("msg = UID %v, MsgID %v, flags %v", m.UID, m.GmailMsgID, m.Flags)
	}
}

func TestSearch_gmail(t *testing.T) {
	tests := []struct {
		name     string
		uid      bool
		criteria imap.SearchCriteria
		want     string
	}{
		{
			name:     "raw",
			criteria: imap.SearchCriteria{GmailRaw: `from:"bob" has:attachment`},
			want:     `SEARCH X-GM-RAW "from:\"bob\" has:attachment"`,
		},
		{
			name:     "raw_non_ascii",
			uid:      true,
			criteria: imap.SearchCriteria{GmailRaw: "subject:Привет"},
			want:     "UID SEARCH CHARSET UTF-8 X-GM-RAW {20}subject:Привет",
		},
		{
			name:     "msgid",
			uid:      true,
			criteria: imap.SearchCriteria{GmailMsgID: 1278455344230334865},
			want:     "UID SEARCH X-GM-MSGID 1278455344230334865",
		},
		{
			name: "combined",
			criteria: imap.SearchCriteria{
				Flag:       []imap.Flag{imap.FlagSeen},
				GmailRaw:   "label:inbox",
				GmailMsgID: 5,
			},
			want: `SEARCH SEEN X-GM-RAW "label:inbox" X-GM-MSGID 5`,
		},
		{
			name: "not_or",
			criteria: imap.SearchCriteria{
				Not: []imap.SearchCriteria{{GmailRaw: "is:starred"}},
				Or: [][2]imap.SearchCriteria{{
					{GmailMsgID: 1},
					{GmailRaw: "in:sent"},
				}},
			},
			want: `SEARCH NOT (X-GM-RAW "is:starred") OR (X-GM-MSGID 1) (X-GM-RAW "in:sent")`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, srv := newScriptedGmailClient(t, func(tag, cmd string) string {
				return "* SEARCH 3 7\r\n"
			})
			var err error
			if tc.uid {
				_, err = client.UIDSearch(&tc.criteria, nil).Wait()
			} else {
				_, err = client.Search(&tc.criteria, nil).Wait()
			}
			if err != nil {
				t.Fatalf("Search().Wait() = %v", err)
			}
			if got := srv.next(t); got != tc.want {
				t.Errorf("command = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStoreGmailLabels(t *testing.T) {
	tests := []struct {
		name   string
		uid    bool
		numSet imap.NumSet
		op     imap.StoreFlagsOp
		silent bool
		labels []string
		want   string
	}{
		{"add", false, imap.SeqSetNum(1), imap.StoreFlagsAdd, false, []string{"Work", "My Label", `\Starred`}, `STORE 1 +X-GM-LABELS ("Work" "My Label" \Starred)`},
		{"del_silent", true, imap.UIDSetNum(5), imap.StoreFlagsDel, true, []string{"Олег"}, `UID STORE 5 -X-GM-LABELS.SILENT ("&BB4EOwQ1BDM-")`},
		{"set_empty", false, imap.SeqSetNum(1), imap.StoreFlagsSet, false, nil, `STORE 1 X-GM-LABELS ()`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, srv := newScriptedGmailClient(t, func(tag, cmd string) string {
				if tc.silent {
					return ""
				}
				return "* 1 FETCH (X-GM-LABELS (Work \"My Label\" \\Starred))\r\n"
			})
			msgs, err := client.StoreGmailLabels(tc.numSet, tc.op, tc.labels, tc.silent).Collect()
			if err != nil {
				t.Fatalf("StoreGmailLabels().Collect() = %v", err)
			}
			if got := srv.next(t); got != tc.want {
				t.Errorf("command = %q, want %q", got, tc.want)
			}
			if tc.silent {
				if len(msgs) != 0 {
					t.Errorf("len(msgs) = %v, want 0", len(msgs))
				}
			} else if len(msgs) != 1 || !reflect.DeepEqual(msgs[0].GmailLabels, []string{"Work", "My Label", `\Starred`}) {
				t.Errorf("msgs = %+v", msgs)
			}
		})
	}
}

func TestStoreGmailLabels_injection(t *testing.T) {
	labels := []string{"\\Inbox) FOO", "\\x\r\nA9 DELETE INBOX", "\\Inbox", "\\ok-1_x"}
	client, srv := newScriptedGmailClient(t, func(tag, cmd string) string { return "" })
	if _, err := client.StoreGmailLabels(imap.SeqSetNum(1), imap.StoreFlagsAdd, labels, true).Collect(); err != nil {
		t.Fatalf("StoreGmailLabels().Collect() = %v", err)
	}
	// CR and LF are modified-UTF-7 encoded, so they never reach the wire raw.
	want := `STORE 1 +X-GM-LABELS.SILENT ("\\Inbox) FOO" "\\x&AA0ACg-A9 DELETE INBOX" \Inbox \ok-1_x)`
	if got := srv.next(t); got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
	// The server saw exactly one command.
	select {
	case extra := <-srv.cmds:
		t.Errorf("unexpected extra command %q", extra)
	default:
	}
}

// zeroReader yields n zero bytes without allocating them.
type zeroReader struct{ n int64 }

func (z *zeroReader) Read(b []byte) (int, error) {
	if z.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(b)) > z.n {
		b = b[:z.n]
	}
	for i := range b {
		b[i] = 0
	}
	z.n -= int64(len(b))
	return len(b), nil
}

// newRawServerClient connects a client to a server goroutine which, for the
// first command, calls respond(tag, conn).
func newRawServerClient(t *testing.T, respond func(tag string, w io.Writer)) *imapclient.Client {
	clientConn, serverConn := net.Pipe()
	go func() {
		defer serverConn.Close()
		br := bufio.NewReader(serverConn)
		io.WriteString(serverConn, "* OK [CAPABILITY IMAP4rev1 X-GM-EXT-1] ready\r\n")
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		tag, _, _ := strings.Cut(line, " ")
		respond(tag, serverConn)
		io.Copy(io.Discard, br)
	}()
	client := imapclient.New(clientConn, nil)
	t.Cleanup(func() { client.Close() })
	if err := client.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting() = %v", err)
	}
	return client
}

func TestFetch_unknownLargeLiteralIsStreamed(t *testing.T) {
	const size = 64 << 20
	client := newRawServerClient(t, func(tag string, w io.Writer) {
		io.WriteString(w, "* 1 FETCH (X-FOO {"+strconv.Itoa(size)+"}\r\n")
		io.Copy(w, &zeroReader{n: size})
		io.WriteString(w, " FLAGS (\\Seen))\r\n"+tag+" OK done\r\n")
	})

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	msgs, err := client.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true}).Collect()
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("Fetch().Collect() = %v", err)
	}
	if len(msgs) != 1 || !reflect.DeepEqual(msgs[0].Flags, []imap.Flag{imap.FlagSeen}) {
		t.Fatalf("msgs = %+v", msgs)
	}
	if delta := after.TotalAlloc - before.TotalAlloc; delta > 8<<20 {
		t.Errorf("allocated %v bytes while skipping a %v byte literal", delta, size)
	}
}

func TestFetch_unknownAttributeTooDeep(t *testing.T) {
	const depth = 1001
	client := newRawServerClient(t, func(tag string, w io.Writer) {
		io.WriteString(w, "* 1 FETCH (X-FOO "+strings.Repeat("(", depth)+strings.Repeat(")", depth)+" FLAGS (\\Seen))\r\n"+tag+" OK done\r\n")
	})
	_, err := client.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true}).Collect()
	if err == nil || !strings.Contains(err.Error(), "depth") {
		t.Errorf("Fetch().Collect() = %v, want a depth error", err)
	}
}

func TestFetch_gmailLabelLimits(t *testing.T) {
	long := strings.Repeat("a", 64*1024+1)
	many := strings.Repeat("a ", 10000) + "a"
	for name, labels := range map[string]string{"long": `("` + long + `")`, "many": "(" + many + ")"} {
		t.Run(name, func(t *testing.T) {
			client := newRawServerClient(t, func(tag string, w io.Writer) {
				io.WriteString(w, "* 1 FETCH (X-GM-LABELS "+labels+")\r\n"+tag+" OK done\r\n")
			})
			_, err := client.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{GmailLabels: true}).Collect()
			if err == nil || !strings.Contains(err.Error(), "X-GM-LABELS") {
				t.Errorf("Fetch().Collect() = %v, want an X-GM-LABELS limit error", err)
			}
		})
	}
}
