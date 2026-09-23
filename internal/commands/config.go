package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/MrFoxMcCloud/devz/internal/cli"
	"github.com/MrFoxMcCloud/devz/internal/config"
)

// Config manages the machine-local configuration file.
func Config() *cli.Command {
	return &cli.Command{
		Name:  "config",
		Short: "show or initialize this machine's devz config",
		Usage: `usage: devz config <show|path|init|edit>

  show    print the effective configuration (file merged over defaults)
  path    print where the file lives
  init    write a starter file if none exists
  edit    open it in $EDITOR

devz ships behavior; the config holds anything that differs between machines --
paths, key ids, which checks apply. That split is what lets one build work for
the whole team.`,
		Run: runConfig,
	}
}

func runConfig(ctx *cli.Context, args []string) error {
	if len(args) == 0 {
		args = []string{"show"}
	}
	switch args[0] {
	case "show":
		data, err := json.MarshalIndent(ctx.Config, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(ctx.Stdout, string(data))
		return nil

	case "path":
		path, err := config.Path()
		if err != nil {
			return err
		}
		fmt.Fprintln(ctx.Stdout, path)
		return nil

	case "init":
		path, err := config.Path()
		if err != nil {
			return err
		}
		if exists(path) {
			return fmt.Errorf("%s already exists", path)
		}
		written, err := config.Save(config.Default())
		if err != nil {
			return err
		}
		fmt.Fprintf(ctx.Stdout, "wrote %s\n", written)
		return nil

	case "edit":
		path, err := config.Path()
		if err != nil {
			return err
		}
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		cmd := exec.Command(editor, path)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()

	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}
