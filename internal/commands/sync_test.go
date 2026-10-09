package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MrFoxMcCloud/devz/internal/cli"
)

func syncEnv(t *testing.T) (ctx *cli.Context, out *bytes.Buffer, home, shared string) {
	t.Helper()
	cfg, _, home := claudeEnv(t)
	shared = filepath.Join(home, ".claude-shared")
	out = &bytes.Buffer{}
	return &cli.Context{Config: cfg, Stdout: out, Stderr: out}, out, home, shared
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSyncLinksAdoptsAndBacksUp(t *testing.T) {
	ctx, out, home, shared := syncEnv(t)
	personal, work := filepath.Join(home, ".claude"), filepath.Join(home, ".claude-work")
	// claudeEnv left a real settings.json in ~/.claude. The shared one is
	// empty, so that content becomes the shared copy.
	mustWrite(t, filepath.Join(personal, "settings.json"), `{"theme":"dark"}`)
	mustWrite(t, filepath.Join(personal, "skills", "one", "SKILL.md"), "a skill")
	// The other account has its own, different, settings: saved, not lost.
	mustWrite(t, filepath.Join(work, "settings.json"), `{"theme":"light"}`)

	if err := runClaude(ctx, []string{"sync"}); err != nil {
		t.Fatalf("sync: %v\n%s", err, out.String())
	}
	if got := mustRead(t, filepath.Join(shared, "settings.json")); got != `{"theme":"dark"}` {
		t.Errorf("shared settings = %s, want the adopted content", got)
	}
	for _, dir := range []string{personal, work} {
		for _, item := range sharedItems {
			if !linksTo(filepath.Join(dir, item.name), filepath.Join(shared, item.name)) {
				t.Errorf("%s/%s is not linked to the shared copy", filepath.Base(dir), item.name)
			}
		}
	}
	if got := mustRead(t, filepath.Join(work, "skills", "one", "SKILL.md")); got != "a skill" {
		t.Errorf("the other account does not see the shared skill: %q", got)
	}
	backups, _ := filepath.Glob(filepath.Join(shared, "backups", "*", ".claude-work_settings.json"))
	if len(backups) != 1 || mustRead(t, backups[0]) != `{"theme":"light"}` {
		t.Errorf("the replaced settings were not backed up: %v", backups)
	}
	for _, r := range checkSync(ctx.Config, claudeAccounts(ctx.Config)) {
		if r.status == statusWarn || r.status == statusFail {
			t.Errorf("doctor after sync: %+v", r)
		}
	}

	// Again: nothing to do, nothing printed but the two headers.
	out.Reset()
	if err := runClaude(ctx, []string{"sync"}); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 2 {
		t.Errorf("second sync changed things:\n%s", out.String())
	}

	// An atomic write replaces the link with a file. Doctor notices, and sync
	// repairs it without losing either version.
	if err := os.Remove(filepath.Join(work, "settings.json")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(work, "settings.json"), `{"theme":"rewritten"}`)
	if rs := checkSync(ctx.Config, claudeAccounts(ctx.Config)); rs[0].status != statusWarn || !strings.Contains(rs[0].detail, "settings.json") {
		t.Errorf("doctor with a clobbered link: %+v", rs[0])
	}
	out.Reset()
	if err := runClaude(ctx, []string{"sync", "--dry-run"}); err != nil || !strings.Contains(out.String(), "would link") {
		t.Errorf("dry run: %v\n%s", err, out.String())
	}
	if linksTo(filepath.Join(work, "settings.json"), filepath.Join(shared, "settings.json")) {
		t.Error("the dry run relinked")
	}
	if err := runClaude(ctx, []string{"sync"}); err != nil {
		t.Fatal(err)
	}
	if !linksTo(filepath.Join(work, "settings.json"), filepath.Join(shared, "settings.json")) ||
		mustRead(t, filepath.Join(shared, "settings.json")) != `{"theme":"dark"}` {
		t.Error("sync did not restore the link to the shared copy")
	}
}

func TestSetTopLevelKeyChangesNothingElse(t *testing.T) {
	doc := "{\n  \"numStartups\": 76,\n  \"big\": 12345678901234567890,\n  \"oauthAccount\": {\n    \"emailAddress\": \"a@b.c\",\n    \"html\": \"<&>\"\n  },\n  \"mcpServers\": {\"old\": {}},\n  \"last\": [1, 2,   3]\n}\n"
	got, err := setTopLevelKey([]byte(doc), "mcpServers", []byte(`{"new": {}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(doc, `{"old": {}}`, `{"new": {}}`, 1)
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// A key that is not there yet goes last.
	got, err = setTopLevelKey([]byte(`{"a": 1}`), "mcpServers", []byte(`{}`))
	if err != nil || string(got) != "{\n  \"a\": 1,\n  \"mcpServers\": {}\n}" {
		t.Errorf("append: %q, %v", got, err)
	}
	for _, bad := range []string{`[1]`, `{"a": `, ``} {
		if _, err := setTopLevelKey([]byte(bad), "k", []byte(`1`)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSyncMCPServers(t *testing.T) {
	ctx, out, home, shared := syncEnv(t)
	servers := `{"crm": {"type": "http", "url": "https://example.com/mcp"}}`
	mustWrite(t, filepath.Join(shared, mcpServersFile), servers+"\n")
	personal := filepath.Join(home, ".claude.json")
	work := filepath.Join(home, ".claude-work", ".claude.json")
	// One account already has them, written differently; the other has an
	// old set and other state that must survive untouched.
	mustWrite(t, personal, `{"oauthAccount":{"emailAddress":"me@personal.dev","organizationName":"Mine"},"mcpServers":{"crm":{"url":"https://example.com/mcp","type":"http"}}}`)
	before := "{\n  \"projects\": {\"/x\": {\"n\": 10000000000}},\n  \"oauthAccount\": {\"emailAddress\": \"me@work.example\", \"organizationName\": \"Work Inc\"},\n  \"mcpServers\": {\"stale\": {}}\n}\n"
	mustWrite(t, work, before)
	personalBefore := mustRead(t, personal)

	if rs := checkSync(ctx.Config, claudeAccounts(ctx.Config)); rs[len(rs)-1].status != statusWarn || !strings.Contains(rs[len(rs)-1].detail, "work") {
		t.Errorf("doctor with drift: %+v", rs[len(rs)-1])
	}
	if err := runClaude(ctx, []string{"sync"}); err != nil {
		t.Fatalf("sync: %v\n%s", err, out.String())
	}
	if mustRead(t, personal) != personalBefore {
		t.Error("an account that was already in sync had its state file rewritten")
	}
	after := mustRead(t, work)
	want := strings.Replace(before, `{"stale": {}}`, "{\n    \"crm\": {\n      \"type\": \"http\",\n      \"url\": \"https://example.com/mcp\"\n    }\n  }", 1)
	if after != want {
		t.Errorf("state file after sync:\n%s\nwant:\n%s", after, want)
	}
	if info, _ := os.Stat(work); info.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %o, want 600", info.Mode().Perm())
	}
	if saved, _ := filepath.Glob(filepath.Join(shared, "backups", "*", ".claude-work_.claude.json")); len(saved) != 1 || mustRead(t, saved[0]) != before {
		t.Errorf("the state file was not backed up before the change: %v", saved)
	}
	if rs := checkSync(ctx.Config, claudeAccounts(ctx.Config)); rs[len(rs)-1].status != statusOK {
		t.Errorf("doctor after sync: %+v", rs[len(rs)-1])
	}

	// pull: an MCP server added in one account becomes everyone's.
	mustWrite(t, work, strings.Replace(mustRead(t, work), `"crm": {`, `"added": {"type": "stdio"}, "crm": {`, 1))
	if err := os.Chmod(work, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runClaude(ctx, []string{"sync", "pull", "work"}); err != nil {
		t.Fatalf("pull: %v\n%s", err, out.String())
	}
	if !strings.Contains(mustRead(t, filepath.Join(shared, mcpServersFile)), `"added"`) ||
		!strings.Contains(mustRead(t, personal), `"added"`) {
		t.Errorf("pull did not carry the new server to the shared file and the other account:\n%s", out.String())
	}

	out.Reset()
	if err := runClaude(ctx, []string{"sync", "status"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Contains(got, "DRIFTED") || strings.Contains(got, "NOT SHARED") ||
		!strings.Contains(got, "me@work.example, Work Inc") || strings.Count(got, "in sync") != 2 {
		t.Errorf("status:\n%s", got)
	}
}

func TestSyncLeavesABrokenStateFileAlone(t *testing.T) {
	ctx, out, home, shared := syncEnv(t)
	mustWrite(t, filepath.Join(shared, mcpServersFile), `{"crm": {}}`)
	// Logged in, but with trailing junk after the object, as a half-written
	// file would have.
	work := filepath.Join(home, ".claude-work", ".claude.json")
	broken := `{"oauthAccount": {"emailAddress": "me@work.example"}, "mcpServers": {"x": {}}, "cut": `
	mustWrite(t, work, broken)
	// It no longer parses, so it is not a logged-in account at all: sync
	// must simply not touch it.
	if err := runClaude(ctx, []string{"sync"}); err != nil {
		t.Fatalf("sync: %v\n%s", err, out.String())
	}
	if mustRead(t, work) != broken {
		t.Error("a state file that does not parse was rewritten")
	}
}

func TestEject(t *testing.T) {
	ctx, out, home, shared := syncEnv(t)
	mustWrite(t, filepath.Join(home, ".claude", "settings.json"), `{"theme":"dark"}`)
	mustWrite(t, filepath.Join(home, ".zshrc"), shellInit)
	if err := runClaude(ctx, []string{"sync"}); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(home, ".claude-work", "settings.json")

	// Without --apply it only describes.
	out.Reset()
	if err := runClaude(ctx, []string{"eject"}); err != nil {
		t.Fatal(err)
	}
	plan := out.String()
	for _, want := range []string{"would replace the link ~/.claude-work/settings.json", "~/.zshrc: remove the claude() function", "nothing was changed"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan lacks %q:\n%s", want, plan)
		}
	}
	if !linksTo(linked, filepath.Join(shared, "settings.json")) {
		t.Fatal("eject without --apply changed something")
	}

	if err := runClaude(ctx, []string{"eject", "--apply"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(linked); err != nil || info.Mode()&os.ModeSymlink != 0 || mustRead(t, linked) != `{"theme":"dark"}` {
		t.Error("the link was not replaced by a copy of the shared settings")
	}
	if info, err := os.Lstat(filepath.Join(home, ".claude", "skills")); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Error("a linked directory was not replaced by a real one")
	}
	if mustRead(t, filepath.Join(shared, "settings.json")) != `{"theme":"dark"}` || !strings.Contains(mustRead(t, filepath.Join(home, ".zshrc")), "claude exec") {
		t.Error("eject removed or edited something it promises to leave")
	}

	// And sync is the way back.
	if err := runClaude(ctx, []string{"sync"}); err != nil {
		t.Fatal(err)
	}
	if !linksTo(linked, filepath.Join(shared, "settings.json")) {
		t.Error("sync did not put the link back after an eject")
	}
}
