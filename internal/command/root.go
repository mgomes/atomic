package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/x/term"
	cli "github.com/urfave/cli/v3"
	"github.com/zeebo/blake3"

	"github.com/mgomes/ressik/internal/application"
	"github.com/mgomes/ressik/internal/backup"
	"github.com/mgomes/ressik/internal/config"
	"github.com/mgomes/ressik/internal/daemon"
	"github.com/mgomes/ressik/internal/osservice"
	"github.com/mgomes/ressik/internal/repository"
	"github.com/mgomes/ressik/internal/tui"
)

// New returns the complete Ressik CLI.
func New(version string, stdout, stderr io.Writer) *cli.Command {
	var configPath string
	root := &cli.Command{
		Name:      "ressik",
		Usage:     "encrypted, deduplicated backups",
		Version:   version,
		Writer:    stdout,
		ErrWriter: stderr,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "config",
				Aliases:     []string{"c"},
				Usage:       "path to config YAML",
				TakesFile:   true,
				Destination: &configPath,
			},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
				return cli.ShowRootCommandHelp(command)
			}
			return runTUI(ctx, configPath)
		},
	}
	root.Commands = []*cli.Command{
		initCommand(&configPath),
		checkCommand(&configPath),
		configCommand(&configPath),
		runCommand(&configPath),
		statusCommand(&configPath),
		snapshotsCommand(&configPath),
		verifyCommand(&configPath),
		garbageCollectCommand(&configPath),
		restoreCommand(&configPath),
		daemonCommand(&configPath),
		serviceCommand(&configPath),
		tuiCommand(&configPath),
	}
	return root
}

func verifyCommand(configPath *string) *cli.Command {
	return &cli.Command{
		Name:  "verify",
		Usage: "authenticate all snapshots and recompute their Merkle trees",
		Action: func(ctx context.Context, command *cli.Command) error {
			verification, err := application.New(*configPath).Verify(ctx)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(
				command.Writer,
				"Verified %d snapshots, %d unique blocks, %d bytes\n",
				verification.Snapshots,
				verification.Blocks,
				verification.BlockBytes,
			)
			return err
		},
	}
}

func garbageCollectCommand(configPath *string) *cli.Command {
	return &cli.Command{
		Name:  "gc",
		Usage: "remove data unreachable from committed snapshots",
		Action: func(ctx context.Context, command *cli.Command) error {
			removed, err := application.New(*configPath).GarbageCollect(ctx)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.Writer, "Removed %d unreferenced blocks\n", removed)
			return err
		},
	}
}

func initCommand(configPath *string) *cli.Command {
	return &cli.Command{
		Name:  "init",
		Usage: "create an empty config and encrypted local repository",
		Action: func(_ context.Context, command *cli.Command) error {
			loaded, err := application.New(*configPath).Initialize()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.Writer, "Created %s\nRepository %s\n", loaded.Path, loaded.Config.Repository)
			return err
		},
	}
}

func checkCommand(configPath *string) *cli.Command {
	var configOnly bool
	return &cli.Command{
		Name:  "check",
		Usage: "validate configuration, repository, and sources",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:        "config-only",
				Usage:       "skip live repository and source checks",
				Destination: &configOnly,
			},
		},
		Action: func(_ context.Context, command *cli.Command) error {
			loaded, err := application.New(*configPath).Check(!configOnly)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.Writer, "Configuration valid: %s\n", loaded.Path)
			return err
		},
	}
}

func configCommand(configPath *string) *cli.Command {
	return &cli.Command{
		Name:  "config",
		Usage: "inspect configuration",
		Commands: []*cli.Command{{
			Name:  "path",
			Usage: "print the resolved config path",
			Action: func(_ context.Context, command *cli.Command) error {
				path, err := config.Path(*configPath)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(command.Writer, path)
				return err
			},
		}},
	}
}

func runCommand(configPath *string) *cli.Command {
	var (
		planID string
		full   bool
	)
	return &cli.Command{
		Name:      "run",
		Usage:     "run one backup plan now",
		ArgsUsage: "<plan>",
		Arguments: []cli.Argument{
			&cli.StringArg{Name: "plan", Destination: &planID},
		},
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "full", Usage: "read and hash every source file", Destination: &full},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			if planID == "" {
				return errors.New("plan is required")
			}
			service := application.New(*configPath)
			var snapshot repository.Summary
			var err error
			if full {
				snapshot, err = service.RunFull(ctx, planID)
			} else {
				snapshot, err = service.Run(ctx, planID)
			}
			if !snapshot.ID.IsZero() {
				if writeErr := printSnapshot(command.Writer, snapshot); writeErr != nil {
					return errors.Join(err, writeErr)
				}
			}
			if err != nil {
				return err
			}
			return nil
		},
	}
}

func snapshotsCommand(configPath *string) *cli.Command {
	var planID string
	return &cli.Command{
		Name:      "snapshots",
		Usage:     "list committed snapshots",
		ArgsUsage: "[plan]",
		Arguments: []cli.Argument{
			&cli.StringArg{Name: "plan", Destination: &planID},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			snapshots, err := application.New(*configPath).Snapshots(ctx, planID)
			if err != nil {
				return err
			}
			if len(snapshots) == 0 {
				_, err = fmt.Fprintln(command.Writer, "No snapshots")
				return err
			}
			for _, snapshot := range snapshots {
				if _, err := fmt.Fprintf(command.Writer, "%s  %-16s  %s  %d files  %d bytes\n",
					snapshot.ID.String(), snapshot.PlanID, snapshot.CreatedAt.Local().Format("2006-01-02 15:04:05"),
					snapshot.Statistics.Files, snapshot.Statistics.PlaintextBytes,
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func restoreCommand(configPath *string) *cli.Command {
	var (
		snapshotID  string
		destination string
	)
	return &cli.Command{
		Name:      "restore",
		Usage:     "restore one committed snapshot",
		ArgsUsage: "<snapshot>",
		Arguments: []cli.Argument{
			&cli.StringArg{Name: "snapshot", Destination: &snapshotID},
		},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "to",
				Usage:       "destination directory",
				Required:    true,
				TakesFile:   true,
				Destination: &destination,
			},
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			if snapshotID == "" {
				return errors.New("snapshot is required")
			}
			if err := application.New(*configPath).Restore(ctx, snapshotID, backup.RestoreOptions{
				Destination: destination,
			}); err != nil {
				return err
			}
			_, err := fmt.Fprintf(command.Writer, "Restored %s to %s\n", snapshotID, destination)
			return err
		},
	}
}

func daemonCommand(configPath *string) *cli.Command {
	var (
		stateDir     string
		managed      bool
		serviceScope string
		logFile      string
	)
	return &cli.Command{
		Name:  "daemon",
		Usage: "run scheduled plans in the foreground",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "state-dir", Usage: "durable daemon state directory", Destination: &stateDir},
			&cli.BoolFlag{Name: "managed", Hidden: true, Destination: &managed},
			&cli.StringFlag{Name: "service-scope", Hidden: true, Value: string(osservice.DefaultScope()), Destination: &serviceScope},
			&cli.StringFlag{Name: "log-file", Hidden: true, Destination: &logFile},
		},
		Action: func(ctx context.Context, _ *cli.Command) error {
			paths, err := runtimePaths(*configPath, stateDir)
			if err != nil {
				return err
			}
			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			var daemonLog *os.File
			if logFile != "" {
				resolvedLog, err := filepath.Abs(logFile)
				if err != nil {
					return fmt.Errorf("resolve daemon log path: %w", err)
				}
				if err := os.MkdirAll(filepath.Dir(resolvedLog), 0o700); err != nil {
					return fmt.Errorf("create daemon log directory: %w", err)
				}
				daemonLog, err = os.OpenFile(resolvedLog, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
				if err != nil {
					return fmt.Errorf("open daemon log: %w", err)
				}
				if err := daemonLog.Chmod(0o600); err != nil {
					_ = daemonLog.Close()
					return fmt.Errorf("protect daemon log: %w", err)
				}
				defer daemonLog.Close()
				logger = slog.New(slog.NewTextHandler(daemonLog, nil))
			}
			runner, err := daemon.New(application.New(paths.config), paths.state, logger)
			if err != nil {
				logger.ErrorContext(ctx, "create daemon", "err", err)
				return err
			}
			if !managed {
				err := runner.Run(ctx)
				if err != nil {
					logger.ErrorContext(ctx, "daemon stopped with an error", "err", err)
				}
				return err
			}
			executable, err := executablePath()
			if err != nil {
				return err
			}
			manager, err := osservice.New(runner, osservice.Options{
				Scope:      osservice.Scope(serviceScope),
				Executable: executable,
				ConfigPath: paths.config,
				StateDir:   paths.state,
			})
			if err != nil {
				logger.ErrorContext(ctx, "create background manager", "err", err)
				return err
			}
			err = manager.Run()
			if err != nil {
				logger.ErrorContext(ctx, "background manager stopped with an error", "err", err)
			}
			return err
		},
	}
}

func serviceCommand(configPath *string) *cli.Command {
	return &cli.Command{
		Name:  "service",
		Usage: "install and control the platform background job",
		Commands: []*cli.Command{
			serviceActionCommand(configPath, osservice.Install),
			serviceActionCommand(configPath, osservice.Start),
			serviceActionCommand(configPath, osservice.Stop),
			serviceActionCommand(configPath, osservice.Restart),
			serviceStatusCommand(configPath),
			serviceActionCommand(configPath, osservice.Uninstall),
		},
	}
}

func serviceActionCommand(configPath *string, action osservice.Action) *cli.Command {
	var (
		stateDir string
		noStart  bool
	)
	flags := []cli.Flag{
		&cli.StringFlag{Name: "state-dir", Usage: "durable daemon state directory", Destination: &stateDir},
	}
	if action == osservice.Install {
		flags = append(flags,
			&cli.BoolFlag{Name: "no-start", Usage: "install without starting", Destination: &noStart},
		)
	}
	return &cli.Command{
		Name:  string(action),
		Usage: string(action) + " the platform background job",
		Flags: flags,
		Action: func(ctx context.Context, command *cli.Command) error {
			paths, err := runtimePaths(*configPath, stateDir)
			if err != nil {
				return err
			}
			if action == osservice.Install {
				if _, err := application.New(paths.config).CheckRepository(); err != nil {
					return err
				}
				if err := validateServiceScope(osservice.DefaultScope()); err != nil {
					return err
				}
				if err := os.MkdirAll(paths.state, 0o700); err != nil {
					return fmt.Errorf("create service state directory: %w", err)
				}
			}
			manager, err := serviceManager(paths, osservice.DefaultScope())
			if err != nil {
				return err
			}
			if err := manager.Control(ctx, action); err != nil {
				return err
			}
			if action == osservice.Install && !noStart {
				if err := manager.Control(ctx, osservice.Start); err != nil {
					rollbackErr := manager.Control(context.WithoutCancel(ctx), osservice.Uninstall)
					return errors.Join(err, rollbackErr)
				}
			}
			_, err = fmt.Fprintf(command.Writer, "Background job %s: %s\n", action, paths.config)
			return err
		},
	}
}

func serviceStatusCommand(configPath *string) *cli.Command {
	var stateDir string
	return &cli.Command{
		Name:  "status",
		Usage: "show background job status",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "state-dir", Usage: "durable daemon state directory", Destination: &stateDir},
		},
		Action: func(_ context.Context, command *cli.Command) error {
			paths, err := runtimePaths(*configPath, stateDir)
			if err != nil {
				return err
			}
			manager, err := serviceManager(paths, osservice.DefaultScope())
			if err != nil {
				return err
			}
			status, err := manager.Status()
			if err != nil && status != osservice.Unknown {
				return err
			}
			_, writeErr := fmt.Fprintln(command.Writer, status)
			return errors.Join(err, writeErr)
		},
	}
}

func tuiCommand(configPath *string) *cli.Command {
	return &cli.Command{
		Name:  "tui",
		Usage: "open the terminal dashboard",
		Action: func(ctx context.Context, _ *cli.Command) error {
			return runTUI(ctx, *configPath)
		},
	}
}

func runTUI(ctx context.Context, configPath string) error {
	paths, err := runtimePaths(configPath, "")
	if err != nil {
		return err
	}
	controller := tui.NewController(application.New(paths.config), paths.state)
	return tui.Run(ctx, controller)
}

type paths struct {
	config string
	state  string
}

func runtimePaths(configPath, stateDir string) (paths, error) {
	resolvedConfig, err := config.Path(configPath)
	if err != nil {
		return paths{}, err
	}
	if physical, evalErr := filepath.EvalSymlinks(resolvedConfig); evalErr == nil {
		resolvedConfig = physical
	}
	if stateDir == "" {
		var base string
		base, err = config.DefaultStateDir()
		if err != nil {
			return paths{}, err
		}
		identity := filepath.Clean(resolvedConfig)
		if runtime.GOOS == "windows" {
			identity = strings.ToLower(identity)
		}
		digest := blake3.Sum256([]byte(identity))
		stateDir = filepath.Join(base, "instances", fmt.Sprintf("%x", digest[:8]))
	}
	resolvedState, err := filepath.Abs(stateDir)
	if err != nil {
		return paths{}, fmt.Errorf("resolve state directory: %w", err)
	}
	return paths{config: resolvedConfig, state: resolvedState}, nil
}

func serviceManager(paths paths, scope osservice.Scope) (*osservice.Manager, error) {
	executable, err := executablePath()
	if err != nil {
		return nil, err
	}
	runner, err := daemon.New(application.New(paths.config), paths.state, slog.Default())
	if err != nil {
		return nil, err
	}
	return osservice.New(runner, osservice.Options{
		Scope:      scope,
		Executable: executable,
		ConfigPath: paths.config,
		StateDir:   paths.state,
	})
}

func executablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find Ressik executable: %w", err)
	}
	return filepath.Abs(path)
}

func validateServiceScope(scope osservice.Scope) error {
	if scope != osservice.UserScope {
		return errors.New("Ressik supports only per-user service scope")
	}
	return nil
}

func printSnapshot(writer io.Writer, snapshot repository.Summary) error {
	lines := []string{
		"Snapshot " + snapshot.ID.String(),
		"Plan " + snapshot.PlanID,
		"Created " + snapshot.CreatedAt.Local().Format("2006-01-02 15:04:05 MST"),
		fmt.Sprintf("Files %d", snapshot.Statistics.Files),
		fmt.Sprintf("Plaintext bytes %d", snapshot.Statistics.PlaintextBytes),
		fmt.Sprintf("Blocks %d new, %d reused", snapshot.Statistics.NewBlocks, snapshot.Statistics.ReusedBlocks),
	}
	_, err := fmt.Fprintln(writer, strings.Join(lines, "\n"))
	return err
}
