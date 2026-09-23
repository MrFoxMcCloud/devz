// Package cli is devz's dispatcher: a registry of built-in commands plus
// discovery of devz-* executables on PATH.
//
// The plugin fallback is the point. Commands everyone on the team should have
// are compiled in; anything personal stays a script named devz-<something> on
// PATH and is reachable the same way, so nobody's one-off workflow has to
// become a pull request.
package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MrFoxMcCloud/devz/internal/config"
)

// Command is one built-in subcommand.
type Command struct {
	Name string
	// Short is the one-line description shown in listings.
	Short string
	// Usage is the full help text for `devz help <name>`.
	Usage string
	// Run receives the arguments after the command name.
	Run func(ctx *Context, args []string) error
}

// Context is what a command is handed.
type Context struct {
	Config config.Config
	Stdout io.Writer
	Stderr io.Writer
	// Version of the running binary.
	Version string
}

// App holds the registry.
type App struct {
	Version  string
	commands []*Command
}

// New returns an App with the given commands registered.
func New(version string, cmds ...*Command) *App {
	return &App{Version: version, commands: cmds}
}

// Register adds commands to the app. Split from New so a command that needs
// the app itself -- completion, which lists every name -- can be built after it.
func (a *App) Register(cmds ...*Command) {
	a.commands = append(a.commands, cmds...)
}

func (a *App) lookup(name string) *Command {
	for _, c := range a.commands {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Run dispatches args (without the program name) and returns an exit code.
func (a *App) Run(args []string) int {
	ctx := &Context{Stdout: os.Stdout, Stderr: os.Stderr, Version: a.Version}

	cfg, err := config.Load()
	if err != nil {
		// A broken config should not stop `devz doctor` from explaining why.
		fmt.Fprintf(ctx.Stderr, "devz: config: %v\n", err)
	}
	ctx.Config = cfg

	if len(args) == 0 {
		a.printOverview(ctx)
		return 0
	}

	name := args[0]
	rest := args[1:]

	switch name {
	case "-h", "--help":
		a.printOverview(ctx)
		return 0
	case "help":
		return a.help(ctx, rest)
	case "-v", "--version":
		name = "version"
	}

	if cmd := a.lookup(name); cmd != nil {
		if err := cmd.Run(ctx, rest); err != nil {
			fmt.Fprintf(ctx.Stderr, "devz %s: %v\n", name, err)
			return 1
		}
		return 0
	}

	// Not built in: try a devz-<name> plugin on PATH.
	if path, err := exec.LookPath("devz-" + name); err == nil {
		return runPlugin(path, rest)
	}

	fmt.Fprintf(ctx.Stderr, "devz: unknown command %q\n", name)
	if s := a.suggest(name); s != "" {
		fmt.Fprintf(ctx.Stderr, "did you mean %q?\n", s)
	}
	fmt.Fprintln(ctx.Stderr, "run 'devz' to see what is available")
	return 127
}

func runPlugin(path string, args []string) int {
	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "devz: %s: %v\n", filepath.Base(path), err)
		return 1
	}
	return 0
}

// Plugin is a devz-* executable discovered on PATH.
type Plugin struct {
	Name string
	Path string
}

// Plugins lists devz-* executables found on PATH, first occurrence winning so
// the listing matches what would actually run.
func Plugins() []Plugin {
	seen := map[string]bool{}
	var out []Plugin
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			base := e.Name()
			if !strings.HasPrefix(base, "devz-") {
				continue
			}
			name := strings.TrimPrefix(base, "devz-")
			if name == "" || seen[name] {
				continue
			}
			path := filepath.Join(dir, base)
			if !executable(path) {
				continue
			}
			seen[name] = true
			out = append(out, Plugin{Name: name, Path: path})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func executable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

// Names returns every dispatchable name, built-in and plugin, for completion.
func (a *App) Names() []string {
	var out []string
	for _, c := range a.commands {
		out = append(out, c.Name)
	}
	for _, p := range Plugins() {
		out = append(out, p.Name)
	}
	out = append(out, "help")
	sort.Strings(out)
	return out
}

func (a *App) printOverview(ctx *Context) {
	w := ctx.Stdout
	fmt.Fprintf(w, "devz %s -- local development environment commands\n\n", a.Version)
	fmt.Fprint(w, "usage: devz <command> [args]\n\n")
	fmt.Fprintln(w, "commands:")
	width := 0
	for _, c := range a.commands {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}
	plugins := Plugins()
	for _, p := range plugins {
		if len(p.Name) > width {
			width = len(p.Name)
		}
	}
	for _, c := range a.commands {
		fmt.Fprintf(w, "  %-*s  %s\n", width, c.Name, c.Short)
	}
	if len(plugins) > 0 {
		fmt.Fprintln(w, "\nplugins (devz-* on PATH):")
		for _, p := range plugins {
			fmt.Fprintf(w, "  %-*s  %s\n", width, p.Name, describePlugin(p.Path))
		}
	}
	fmt.Fprintln(w, "\nrun 'devz help <command>' for detail, or 'devz doctor' to check this machine.")
}

// describePlugin reads a plugin's one-line description from a `# devz: ...`
// comment in its first few lines, so plugins can describe themselves in the
// listing without devz having to execute them.
func describePlugin(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.SplitN(string(data), "\n", 12)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, "devz:"); idx >= 0 && strings.HasPrefix(line, "#") {
			return strings.TrimSpace(line[idx+len("devz:"):])
		}
	}
	return ""
}

func (a *App) help(ctx *Context, args []string) int {
	if len(args) == 0 {
		a.printOverview(ctx)
		return 0
	}
	name := args[0]
	if cmd := a.lookup(name); cmd != nil {
		usage := cmd.Usage
		if usage == "" {
			usage = cmd.Short
		}
		fmt.Fprintln(ctx.Stdout, strings.TrimSpace(usage))
		return 0
	}
	if path, err := exec.LookPath("devz-" + name); err == nil {
		// Plugins own their help; ask them for it.
		return runPlugin(path, []string{"--help"})
	}
	fmt.Fprintf(ctx.Stderr, "devz: unknown command %q\n", name)
	return 127
}

// suggest returns the closest known name within edit distance 2.
func (a *App) suggest(name string) string {
	best, bestDist := "", 3
	for _, candidate := range a.Names() {
		if d := distance(name, candidate); d < bestDist {
			best, bestDist = candidate, d
		}
	}
	return best
}

func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
