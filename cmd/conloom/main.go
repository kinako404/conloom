// Command conloom runs the ConLoom core service and its adapters.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	_ "time/tzdata"

	"github.com/kinako404/conloom/internal/adapter/mock"
	"github.com/kinako404/conloom/internal/app"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/client"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// Set with -ldflags "-X main.version=… -X main.commit=…".
var (
	version = "dev"
	commit  = "unknown"
)

const (
	exitRuntime = 1
	exitConfig  = 2
)

const usage = `usage:
  conloom serve -c conloom.yaml
  conloom adapter napcat -c conloom.yaml
  conloom mock -c conloom.yaml [-conv group:10001] [-user 20001:name]
  conloom backup -c conloom.yaml -o <file>
  conloom version
`

// exitError carries the process exit code for err.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// configErr marks err as a usage or configuration problem (exit code 2).
func configErr(err error) error { return &exitError{code: exitConfig, err: err} }

type stdio struct {
	in       io.Reader
	out, err io.Writer
}

type command func(ctx context.Context, args []string, sio stdio) error

var commands = map[string]command{
	"serve":   runServe,
	"adapter": runAdapter,
	"mock":    runMock,
	"backup":  runBackup,
	"version": runVersion,
}

var adapters = map[string]command{
	"napcat": runNapCat,
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], stdio{in: os.Stdin, out: os.Stdout, err: os.Stderr})
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, sio stdio) int {
	if len(args) == 0 {
		fmt.Fprint(sio.err, usage)
		return exitConfig
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		fmt.Fprint(sio.out, usage)
		return 0
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(sio.err, "conloom: unknown command %q\n%s", args[0], usage)
		return exitConfig
	}
	err := cmd(ctx, args[1:], sio)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	}
	fmt.Fprintf(sio.err, "conloom %s: %v\n", args[0], err)
	if ee := (*exitError)(nil); errors.As(err, &ee) {
		return ee.code
	}
	return exitRuntime
}

// parseFlags parses args into fs and rejects positional arguments.
func parseFlags(fs *flag.FlagSet, args []string, sio stdio) error {
	fs.SetOutput(sio.err)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return configErr(err)
	}
	if fs.NArg() > 0 {
		return configErr(fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " ")))
	}
	return nil
}

func loadConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, configErr(err)
	}
	return cfg, nil
}

func runServe(ctx context.Context, args []string, sio stdio) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("c", "conloom.yaml", "config file")
	if err := parseFlags(fs, args, sio); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if err := cfg.ValidateServe(); err != nil {
		return configErr(err)
	}
	logger := obs.NewLogger(cfg.Observability, sio.err)
	logger.Info("starting conloom", "version", version, "commit", commit, "config", cfg.Path)
	return app.Serve(ctx, cfg, logger)
}

func runAdapter(ctx context.Context, args []string, sio stdio) error {
	if len(args) == 0 {
		return configErr(errors.New("missing adapter name; usage: conloom adapter napcat -c conloom.yaml"))
	}
	adapter, ok := adapters[args[0]]
	if !ok {
		return configErr(fmt.Errorf("unknown adapter %q; usage: conloom adapter napcat -c conloom.yaml", args[0]))
	}
	return adapter(ctx, args[1:], sio)
}

func runNapCat(context.Context, []string, stdio) error {
	return configErr(errors.New("napcat: not implemented"))
}

func runMock(ctx context.Context, args []string, sio stdio) error {
	fs := flag.NewFlagSet("mock", flag.ContinueOnError)
	cfgPath := fs.String("c", "conloom.yaml", "config file")
	convFlag := fs.String("conv", "", "conversation as type:id, e.g. group:10001 (default: private chat with the user)")
	userFlag := fs.String("user", "", "user as id:name, e.g. 20001:name (default: the bot's first admin)")
	if err := parseFlags(fs, args, sio); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if err := cfg.ValidateMock(); err != nil {
		return configErr(err)
	}
	mc := cfg.Adapters.Mock
	bot, ok := cfg.Bot(mc.BotInstanceID)
	if !ok {
		return configErr(fmt.Errorf("adapters.mock.bot_instance_id: unknown bot %q", mc.BotInstanceID))
	}
	user, err := mockUser(*userFlag, bot)
	if err != nil {
		return configErr(err)
	}
	conv, err := mockConv(*convFlag, user)
	if err != nil {
		return configErr(err)
	}

	m := mock.New(client.New(mc.CoreURL, mc.CoreToken, nil), bot.ID, bot.Platform)
	fmt.Fprintf(sio.err, "会话 %s_%s，用户 %s（%s）。每行一条消息，以 @ 开头表示 @Bot；输入结束（EOF）退出。\n",
		conv.Type, conv.PlatformID, user.Name, user.PlatformID)
	err = mock.RunREPL(ctx, m, conv, user, sio.in, sio.out)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func mockUser(flagValue string, bot config.BotConfig) (mock.User, error) {
	if flagValue == "" {
		if len(bot.Admins) == 0 {
			return mock.User{}, fmt.Errorf("bot %q has no admins; pass -user id:name", bot.ID)
		}
		return mock.User{PlatformID: bot.Admins[0], Name: "管理员"}, nil
	}
	id, name, _ := strings.Cut(flagValue, ":")
	if strings.TrimSpace(id) == "" {
		return mock.User{}, fmt.Errorf("-user must be id:name, got %q", flagValue)
	}
	if name == "" {
		name = id
	}
	return mock.User{PlatformID: id, Name: name}, nil
}

func mockConv(flagValue string, user mock.User) (mock.Conv, error) {
	if flagValue == "" {
		return mock.Conv{Type: conloomapi.ConvPrivate, PlatformID: user.PlatformID}, nil
	}
	typ, id, _ := strings.Cut(flagValue, ":")
	if (typ != conloomapi.ConvGroup && typ != conloomapi.ConvPrivate) || strings.TrimSpace(id) == "" {
		return mock.Conv{}, fmt.Errorf("-conv must be group:<id> or private:<id>, got %q", flagValue)
	}
	return mock.Conv{Type: typ, PlatformID: id}, nil
}

func runBackup(ctx context.Context, args []string, sio stdio) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	cfgPath := fs.String("c", "conloom.yaml", "config file")
	out := fs.String("o", "", "backup file to create (must not exist)")
	if err := parseFlags(fs, args, sio); err != nil {
		return err
	}
	if *out == "" {
		return configErr(errors.New("-o <file> is required"))
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	db, err := sqlite.Open(ctx, cfg.DBPath())
	if err != nil {
		return fmt.Errorf("open database %s: %w", cfg.DBPath(), err)
	}
	if err := db.Backup(ctx, *out); err != nil {
		_ = db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	fmt.Fprintf(sio.out, "backup of %s written to %s\n", cfg.DBPath(), *out)
	return nil
}

func runVersion(_ context.Context, args []string, sio stdio) error {
	if err := parseFlags(flag.NewFlagSet("version", flag.ContinueOnError), args, sio); err != nil {
		return err
	}
	fmt.Fprintf(sio.out, "conloom %s (commit %s)\n", version, commit)
	return nil
}
