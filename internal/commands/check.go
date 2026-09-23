package commands

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// status is a check outcome.
type status int

const (
	statusOK status = iota
	statusWarn
	statusFail
	statusSkip
)

func (s status) label(color bool) string {
	text := map[status]string{
		statusOK: " ok ", statusWarn: "warn", statusFail: "FAIL", statusSkip: "skip",
	}[s]
	if !color {
		return "[" + text + "]"
	}
	code := map[status]string{
		statusOK: "32", statusWarn: "33", statusFail: "31", statusSkip: "90",
	}[s]
	return "[\033[" + code + "m" + text + "\033[0m]"
}

// result is one line of `devz doctor` output.
type result struct {
	id     string
	status status
	detail string
	// fix is the command or action that resolves a warn/fail.
	fix string
}

func ok(id, detail string) result   { return result{id: id, status: statusOK, detail: detail} }
func skip(id, detail string) result { return result{id: id, status: statusSkip, detail: detail} }

func warn(id, detail, fix string) result {
	return result{id: id, status: statusWarn, detail: detail, fix: fix}
}

func fail(id, detail, fix string) result {
	return result{id: id, status: statusFail, detail: detail, fix: fix}
}

func (r result) write(w io.Writer, width int, color bool) {
	fmt.Fprintf(w, "%s %-*s %s\n", r.status.label(color), width, r.id, r.detail)
	if r.fix != "" {
		fmt.Fprintf(w, "       %-*s → %s\n", width, "", r.fix)
	}
}

// look reports a tool's resolved path.
func look(tool string) (string, bool) {
	path, err := exec.LookPath(tool)
	return path, err == nil
}

// output runs a command and returns its trimmed combined stdout.
func output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Stderr = nil
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// colorEnabled reports whether to emit ANSI codes: a TTY, and not NO_COLOR.
func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, isFile := w.(*os.File)
	if !isFile {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
