package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// WriteError renders a safe error summary and any optional recovery guidance.
// Callers provide the diagnostic stream, not the command's result stream.
func WriteError(out io.Writer, err error) error {
	terminal := isTerminal(out)
	color := terminal && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	label := func(name, code string) string {
		if color {
			return "\x1b[" + code + "m" + name + ":\x1b[0m"
		}
		return name + ":"
	}

	var message strings.Builder
	fmt.Fprintf(&message, "%s %s\n", label("error", "1;31"), err)
	var help interface{ Help() string }
	var note interface{ Note() string }
	var helpText, noteText string
	if errors.As(err, &help) {
		helpText = help.Help()
	}
	if errors.As(err, &note) {
		noteText = note.Note()
	}
	separated := false
	for _, detail := range []struct{ name, text string }{
		{"help", helpText},
		{"note", noteText},
	} {
		if detail.text == "" {
			continue
		}
		if !separated {
			message.WriteByte('\n')
			separated = true
		}
		fmt.Fprintf(&message, "  %s %s\n", label(detail.name, "1;36"), detail.text)
	}
	_, writeErr := io.WriteString(out, message.String())
	return writeErr
}
