package command

import (
	"context"
	"time"

	"charm.land/lipgloss/v2"
	cli "github.com/urfave/cli/v3"

	"github.com/mgomes/atomic/internal/status"
)

func statusCommand(configPath *string) *cli.Command {
	var stateDir string
	return &cli.Command{
		Name:  "status",
		Usage: "show recent scheduled backup outcomes",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "state-dir",
				Usage:       "durable daemon state directory",
				Destination: &stateDir,
			},
		},
		Action: func(_ context.Context, command *cli.Command) error {
			paths, err := runtimePaths(*configPath, stateDir)
			if err != nil {
				return err
			}
			report, err := status.Load(paths.config, paths.state, time.Now())
			if err != nil {
				return err
			}
			_, err = lipgloss.Fprintln(command.Writer, status.Render(report))
			return err
		},
	}
}
