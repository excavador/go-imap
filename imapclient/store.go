package imapclient

import (
	"fmt"
	"regexp"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/internal/imapwire"
	"github.com/emersion/go-imap/v2/internal/utf7"
)

// Store sends a STORE command.
//
// Unless StoreFlags.Silent is set, the server will return the updated values.
//
// A nil options pointer is equivalent to a zero options value.
func (c *Client) Store(numSet imap.NumSet, store *imap.StoreFlags, options *imap.StoreOptions) *FetchCommand {
	cmd := &FetchCommand{
		numSet: numSet,
		msgs:   make(chan *FetchMessageData, 128),
	}
	enc := c.beginCommand(uidCmdName("STORE", imapwire.NumSetKind(numSet)), cmd)
	enc.SP().NumSet(numSet).SP()
	if options != nil && options.UnchangedSince != 0 {
		enc.Special('(').Atom("UNCHANGEDSINCE").SP().ModSeq(options.UnchangedSince).Special(')').SP()
	}
	switch store.Op {
	case imap.StoreFlagsSet:
		// nothing to do
	case imap.StoreFlagsAdd:
		enc.Special('+')
	case imap.StoreFlagsDel:
		enc.Special('-')
	default:
		panic(fmt.Errorf("imapclient: unknown store flags op: %v", store.Op))
	}
	enc.Atom("FLAGS")
	if store.Silent {
		enc.Atom(".SILENT")
	}
	enc.SP().List(len(store.Flags), func(i int) {
		enc.Flag(store.Flags[i])
	})
	enc.end()
	return cmd
}

// gmailSystemLabelRe matches system labels such as \Inbox, which are sent as
// bare atoms. Anything else is sent as a string.
var gmailSystemLabelRe = regexp.MustCompile(`^\\[A-Za-z][A-Za-z0-9_-]*$`)

// StoreGmailLabels sends a STORE command altering Gmail labels
// (X-GM-LABELS).
//
// Unless silent is set, the server will return the updated labels.
//
// This requires the X-GM-EXT-1 extension.
func (c *Client) StoreGmailLabels(numSet imap.NumSet, op imap.StoreFlagsOp, labels []string, silent bool) *FetchCommand {
	cmd := &FetchCommand{
		numSet: numSet,
		msgs:   make(chan *FetchMessageData, 128),
	}
	enc := c.beginCommand(uidCmdName("STORE", imapwire.NumSetKind(numSet)), cmd)
	enc.SP().NumSet(numSet).SP()
	switch op {
	case imap.StoreFlagsSet:
		// nothing to do
	case imap.StoreFlagsAdd:
		enc.Special('+')
	case imap.StoreFlagsDel:
		enc.Special('-')
	default:
		panic(fmt.Errorf("imapclient: unknown store flags op: %v", op))
	}
	enc.Atom("X-GM-LABELS")
	if silent {
		enc.Atom(".SILENT")
	}
	enc.SP().List(len(labels), func(i int) {
		label := labels[i]
		if gmailSystemLabelRe.MatchString(label) {
			// System label, sent verbatim like a flag
			enc.Atom(label)
		} else if enc.QuotedUTF8 {
			enc.String(utf7.Escape(label))
		} else {
			enc.String(utf7.Encode(label))
		}
	})
	enc.end()
	return cmd
}
