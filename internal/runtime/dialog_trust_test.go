package runtime

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// claudeTrustDialogNoFirst is the pane Claude Code 2.1.296 shows, captured from
// a dead worker's start-stderr.log (2026-10-10). "No, exit" is listed first and
// carries the cursor, so a bare Enter exits.
const claudeTrustDialogNoFirst = `────────────────────────────────────────────────────────────────────────────────
 Accessing workspace:

 /home/corsolvtech/corsolv-delivery/aws-infrastructure-intelligence/city-202610
 10T201228Z/.gc/agents/bd.dog-1

 Quick safety check: Is this a project you created or one you trust? (Like your
 own code, a well-known open source project, or work from your team). If not,
 take a moment to review what's in this folder first.

 Claude Code'll be able to read, edit, and execute files here.

 Security guide

 ❯ No, exit
   Yes, I trust this folder

 Enter to confirm · Esc to cancel
`

// claudeTrustDialogYesFirst is the earlier Claude Code 2.x ordering, where the
// trust option is first and pre-selected.
const claudeTrustDialogYesFirst = `────────────────────────────────────────────────────────────────────────────────
 Accessing workspace:

 /home/user/city

 Quick safety check: Is this a project you created or one you trust? (Like your
 own code, a well-known open source project, or work from your team). If not,
 take a moment to review what's in this folder first.

 Claude Code'll be able to read, edit, and execute files here.

 Security guide

 ❯ 1. Yes, I trust this folder
   2. No, exit

 Enter to confirm · Esc to cancel
`

func TestWorkspaceTrustDialogKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "claude 2.1.296 no first",
			content: claudeTrustDialogNoFirst,
			want:    []string{"Down", "Enter"},
		},
		{
			name:    "claude yes first",
			content: claudeTrustDialogYesFirst,
			want:    []string{"Enter"},
		},
		{
			name:    "claude 1.x yes proceed",
			content: "Do you trust the files in this folder?\n\n ❯ 1. Yes, proceed\n   2. No, exit\n",
			want:    []string{"Enter"},
		},
		{
			name:    "cursor below yes option",
			content: "Quick safety check\n\n   Yes, I trust this folder\n ❯ No, exit\n",
			want:    []string{"Up", "Enter"},
		},
		{
			name:    "yes two rows below cursor",
			content: "Quick safety check\n\n ❯ No, exit\n   Maybe later\n   Yes, I trust this folder\n",
			want:    []string{"Down", "Down", "Enter"},
		},
		{
			name:    "boxed menu",
			content: "│ Quick safety check            │\n│                               │\n│ ❯ No, exit                    │\n│   Yes, I trust this folder    │\n",
			want:    []string{"Down", "Enter"},
		},
		{
			name: "earlier dialog in scrollback uses latest menu",
			content: claudeTrustDialogYesFirst + "\n\n" +
				claudeTrustDialogNoFirst,
			want: []string{"Down", "Enter"},
		},
		{
			name:    "codex trust dialog",
			content: "Do you trust the contents of this directory?\n\n› 1. Yes, continue\n  2. No, quit\n",
			want:    []string{"Enter"},
		},
		{
			name:    "gemini trust dialog",
			content: "Do you trust the files in this folder?\n● 1. Trust folder (city)\n  2. Trust parent folder\n  3. Don't trust",
			want:    []string{"Enter"},
		},
		{
			name:    "pi trust dialog",
			content: "Trust project folder?\n/home/user/project\n\n→ Trust\n  Trust parent folder (/home/user)\n  Do not trust",
			want:    []string{"Enter"},
		},
		{
			name:    "unparsed menu falls back to enter",
			content: "Quick safety check\ntrust this folder",
			want:    []string{"Enter"},
		},
		{
			name:    "cursor on reject with no trust label sends nothing",
			content: "Quick safety check: do you trust this folder?\n\n ❯ No, exit\n   Something unrecognized\n",
			want:    nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := workspaceTrustDialogKeys(tt.content); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("workspaceTrustDialogKeys() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAcceptStartupDialogsSelectsClaudeTrustOptionByLabel(t *testing.T) {
	for _, tc := range []struct {
		name string
		pane string
		want []string
	}{
		{name: "no first", pane: claudeTrustDialogNoFirst, want: []string{"Down", "Enter"}},
		{name: "yes first", pane: claudeTrustDialogYesFirst, want: []string{"Enter"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withZeroDialogTimings(t)
			dialogPollTimeout = time.Second

			var sent []string
			err := AcceptStartupDialogs(
				context.Background(),
				func(_ int) (string, error) {
					if len(sent) < len(tc.want) {
						return tc.pane, nil
					}
					return "❯ ", nil
				},
				func(keys ...string) error {
					sent = append(sent, keys...)
					return nil
				},
			)
			if err != nil {
				t.Fatalf("AcceptStartupDialogs() error = %v", err)
			}
			if !reflect.DeepEqual(sent, tc.want) {
				t.Fatalf("sent keys = %v, want %v", sent, tc.want)
			}
		})
	}
}

func TestAcceptStartupDialogsFromStreamSelectsClaudeTrustOptionByLabel(t *testing.T) {
	for _, tc := range []struct {
		name string
		pane string
		want []string
	}{
		{name: "no first", pane: claudeTrustDialogNoFirst, want: []string{"Down", "Enter"}},
		{name: "yes first", pane: claudeTrustDialogYesFirst, want: []string{"Enter"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withZeroDialogTimings(t)

			snapshots := make(chan string, 2)
			snapshots <- tc.pane
			snapshots <- "❯ "
			close(snapshots)

			var sent []string
			err := AcceptStartupDialogsFromStream(
				context.Background(),
				time.Second,
				snapshots,
				func(keys ...string) error {
					sent = append(sent, keys...)
					return nil
				},
			)
			if err != nil {
				t.Fatalf("AcceptStartupDialogsFromStream() error = %v", err)
			}
			if !reflect.DeepEqual(sent, tc.want) {
				t.Fatalf("sent keys = %v, want %v", sent, tc.want)
			}
		})
	}
}
