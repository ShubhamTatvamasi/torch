package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/statvamasi/torch/internal/runner"
)

// ui is a tiny renderer. No dependency, no layout engine: a terminal tool
// should stay usable over plain pipes and in CI logs.
type ui struct {
	out   io.Writer
	err   io.Writer
	color bool
}

func newUI(out, errOut io.Writer) *ui {
	return &ui{out: out, err: errOut, color: useColor(out)}
}

func useColor(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func (u *ui) paint(code, s string) string {
	if !u.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (u *ui) bold(s string) string   { return u.paint("1", s) }
func (u *ui) dim(s string) string    { return u.paint("2", s) }
func (u *ui) green(s string) string  { return u.paint("32", s) }
func (u *ui) red(s string) string    { return u.paint("31", s) }
func (u *ui) yellow(s string) string { return u.paint("33", s) }
func (u *ui) cyan(s string) string   { return u.paint("36", s) }
func (u *ui) magenta(s string) string {
	return u.paint("35", s)
}

func (u *ui) printf(format string, args ...any) {
	fmt.Fprintf(u.out, format, args...)
}

func (u *ui) errorf(format string, args ...any) {
	fmt.Fprintf(u.err, format, args...)
}

// section prints a heading with a rule underneath.
func (u *ui) section(title string) {
	width := len([]rune(title)) + 2
	if width < 12 {
		width = 12
	}
	u.printf("\n%s\n%s\n", u.bold(title), u.dim(strings.Repeat("─", width)))
}

// review shows the judge's verdict.
func (u *ui) review(d *runner.Decision) {
	if d.Verdict == nil {
		return
	}
	reviewed := d.ReviewedStep
	if reviewed == nil {
		reviewed = d.Step
	}
	title := "Review"
	if reviewed != nil {
		title = fmt.Sprintf("Review · step %s · %s", reviewed.ID, reviewed.Title)
	}
	u.section(title)

	verdict := d.Verdict.Verdict
	label := verdict
	switch verdict {
	case "done":
		label = u.green(verdict)
	case "partial":
		label = u.yellow(verdict)
	case "blocked":
		label = u.red(verdict)
	case "off_track":
		label = u.red(verdict)
	}
	u.printf("%s  %s\n", u.bold(label), u.dim(fmt.Sprintf("confidence %.2f", d.Verdict.Confidence)))
	if notes := strings.TrimSpace(d.Verdict.Notes); notes != "" {
		u.printf("%s\n", notes)
	}
	if len(d.Verdict.Evidence) > 0 {
		u.printf("\n%s\n", u.dim("evidence"))
		for _, item := range d.Verdict.Evidence {
			u.printf("  • %s\n", item)
		}
	}
	if len(d.Verdict.Remaining) > 0 {
		u.printf("\n%s\n", u.dim("remaining"))
		for _, item := range d.Verdict.Remaining {
			u.printf("  • %s\n", item)
		}
	}
}

// instruction prints the next brief.
func (u *ui) instruction(d *runner.Decision) {
	if strings.TrimSpace(d.Instruction) == "" {
		return
	}
	label := "Next instruction"
	if d.Step != nil {
		label = fmt.Sprintf("Next instruction → step %s · %s", d.Step.ID, d.Step.Title)
	}
	u.section(label)
	u.printf("%s\n", strings.TrimRight(d.Instruction, "\n"))
}

// decision renders a whole decision, including terminal states.
func (u *ui) decision(d *runner.Decision, sentTo string, send bool) {
	u.review(d)
	u.instruction(d)

	switch d.Action {
	case runner.ActionComplete:
		u.printf("\n%s\n", u.green("Plan complete. Nothing left to instruct."))
	case runner.ActionBlocked:
		reason := ""
		if d.Step != nil {
			reason = strings.TrimSpace(d.Step.Notes)
		}
		if reason != "" {
			u.printf("\n%s %s\n", u.red("Blocked:"), reason)
		} else {
			u.printf("\n%s\n", u.red("Blocked. Needs a human decision."))
		}
		u.printf("%s\n", u.dim("Resolve it, then run `torch unblock <step-id>` to queue a fresh attempt."))
	}
	if sentTo != "" {
		u.printf("\n%s %s\n", u.green("Sent →"), u.bold(sentTo))
	} else if strings.TrimSpace(d.Instruction) != "" && !send && d.Action != runner.ActionComplete {
		u.printf("\n%s\n", u.dim("Dry run. Re-run with --send to deliver this instruction."))
	}
}
