package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	needle "github.com/zbiljic/needle-go"
)

const maxInputSize = 8 << 20

var version = "dev"

type dependencies struct {
	newAgent        func(context.Context, needle.Config) (needle.Agent, error)
	fetchEngine     func(context.Context, needle.FetchOptions) (string, error)
	cachedEngine    func(needle.FetchOptions) (string, error)
	currentPlatform func() (needle.Platform, error)
}

type application struct {
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
	interactive bool
	deps        dependencies
}

type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func main() {
	interactive := false
	if info, err := os.Stdin.Stat(); err == nil {
		interactive = info.Mode()&os.ModeCharDevice != 0
	}
	app := application{
		stdin:       os.Stdin,
		stdout:      os.Stdout,
		stderr:      os.Stderr,
		interactive: interactive,
		deps: dependencies{
			newAgent:        needle.New,
			fetchEngine:     needle.FetchEngine,
			cachedEngine:    needle.CachedEngine,
			currentPlatform: needle.CurrentPlatform,
		},
	}
	os.Exit(app.run(context.Background(), os.Args[1:]))
}

func (a *application) run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.printUsage()
		return 2
	}
	var err error
	switch args[0] {
	case "help", "-h", "--help":
		a.printUsage()
		return 0
	case "fetch":
		err = a.runFetch(ctx, args[1:])
	case "complete":
		err = a.runComplete(ctx, args[1:])
	case "repl":
		err = a.runREPL(ctx, args[1:])
	case "doctor":
		err = a.runDoctor(ctx, args[1:])
	case "version":
		err = a.runVersion(args[1:])
	default:
		err = usageError{fmt.Errorf("unknown command %q", args[0])}
	}
	if err == nil {
		return 0
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	fmt.Fprintf(a.stderr, "needlez: %v\n", err)
	var usage usageError
	if errors.As(err, &usage) {
		return 2
	}
	return 1
}

func (a *application) printUsage() {
	fmt.Fprint(a.stderr, `Usage: needlez <command> [options]

Commands:
  fetch       Download and verify a native Needle engine
  complete    Perform one raw model completion
  repl        Exercise a persistent model session interactively
  doctor      Diagnose the local engine installation
  version     Print CLI, engine, and platform versions

Run "needlez <command> -h" for command-specific options.
`)
}

func (a *application) flagSet(description, synopsis string) *flag.FlagSet {
	command := strings.Fields(synopsis)[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.Usage = func() {
		fmt.Fprintf(a.stderr, "Usage: needlez %s\n\n%s\n\nOptions:\n", synopsis, description)
		flags.PrintDefaults()
	}
	return flags
}

func parseFlags(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{err}
	}
	return nil
}

func (a *application) runFetch(ctx context.Context, args []string) error {
	flags := a.flagSet("Download the engine for this or another desktop platform.", "fetch [options]")
	platform := flags.String("platform", "", "target platform; empty selects the current platform")
	cacheDir := flags.String("cache", "", "engine cache directory")
	list := flags.Bool("list", false, "list supported platforms without downloading")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{errors.New("fetch does not accept positional arguments")}
	}
	if *list {
		for _, supported := range needle.SupportedPlatforms() {
			fmt.Fprintln(a.stdout, supported)
		}
		return nil
	}
	path, err := a.deps.fetchEngine(ctx, needle.FetchOptions{
		Platform: needle.Platform(*platform),
		CacheDir: *cacheDir,
	})
	if err != nil {
		return fmt.Errorf("fetch engine: %w", err)
	}
	fmt.Fprintln(a.stdout, path)
	return nil
}

type agentOptions struct {
	libraryPath string
	cacheDir    string
	weightsPath string
	toolsPath   string
	system      string
	systemFile  string
	toolIndex   string
	bufferSize  int
	maxTokens   int
	pretty      bool
}

func addAgentFlags(flags *flag.FlagSet, options *agentOptions) {
	flags.StringVar(&options.libraryPath, "library", "", "path to libneedle shared library")
	flags.StringVar(&options.cacheDir, "cache", "", "engine cache directory")
	flags.StringVar(&options.weightsPath, "weights", "", "path to tuned .cact weights")
	flags.StringVar(&options.toolsPath, "tools", "", "path to a JSON array of tool schemas")
	flags.StringVar(&options.system, "system", "", "session facts supplied as text")
	flags.StringVar(&options.systemFile, "system-file", "", "path to session facts")
	flags.StringVar(&options.toolIndex, "tool-index", "", "path to persistent tool embeddings")
	flags.IntVar(&options.bufferSize, "buffer-size", 0, "native response buffer size")
	flags.IntVar(&options.maxTokens, "max-tokens", needle.DefaultMaxNewTokens, "response token limit")
	flags.BoolVar(&options.pretty, "pretty", false, "indent response JSON")
}

func (a *application) agentConfig(options agentOptions) (needle.Config, error) {
	if options.system != "" && options.systemFile != "" {
		return needle.Config{}, errors.New("--system and --system-file are mutually exclusive")
	}
	system := options.system
	if options.systemFile != "" {
		data, err := readFileLimited(options.systemFile, maxInputSize)
		if err != nil {
			return needle.Config{}, fmt.Errorf("read system file: %w", err)
		}
		system = string(data)
	}
	var tools []needle.Tool
	if options.toolsPath != "" {
		data, err := readFileLimited(options.toolsPath, maxInputSize)
		if err != nil {
			return needle.Config{}, fmt.Errorf("read tools: %w", err)
		}
		var schemas []needle.ToolSchema
		if err := json.Unmarshal(data, &schemas); err != nil {
			return needle.Config{}, fmt.Errorf("decode tools: %w", err)
		}
		tools = make([]needle.Tool, 0, len(schemas))
		for _, schema := range schemas {
			tools = append(tools, needle.Tool{Schema: schema})
		}
	}
	return needle.Config{
		Tools:         tools,
		System:        system,
		WeightsPath:   options.weightsPath,
		ToolIndexPath: options.toolIndex,
		BufferSize:    options.bufferSize,
		LibraryPath:   options.libraryPath,
		CacheDir:      options.cacheDir,
	}, nil
}

func (a *application) runComplete(ctx context.Context, args []string) error {
	flags := a.flagSet("Perform one model turn and print its response envelope.", "complete [options] [prompt]")
	var options agentOptions
	addAgentFlags(flags, &options)
	promptFlag := flags.String("prompt", "", "input prompt; alternatively use arguments or stdin")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if *promptFlag != "" && flags.NArg() != 0 {
		return usageError{errors.New("use either --prompt or positional prompt arguments")}
	}
	prompt := *promptFlag
	if flags.NArg() != 0 {
		prompt = strings.Join(flags.Args(), " ")
	}
	if prompt == "" {
		data, err := readLimited(a.stdin, maxInputSize)
		if err != nil {
			return fmt.Errorf("read prompt: %w", err)
		}
		prompt = string(data)
	}
	if strings.TrimSpace(prompt) == "" {
		return usageError{errors.New("prompt is empty")}
	}
	config, err := a.agentConfig(options)
	if err != nil {
		return err
	}
	agent, err := a.deps.newAgent(ctx, config)
	if err != nil {
		return fmt.Errorf("initialize agent: %w", err)
	}
	response, err := agent.Complete(ctx, prompt, options.maxTokens)
	if err != nil {
		return fmt.Errorf("complete: %w", err)
	}
	return writeJSON(a.stdout, response, options.pretty)
}

func (a *application) runREPL(ctx context.Context, args []string) error {
	flags := a.flagSet("Run a persistent manual completion session.", "repl [options]")
	var options agentOptions
	addAgentFlags(flags, &options)
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{errors.New("repl does not accept positional arguments")}
	}
	config, err := a.agentConfig(options)
	if err != nil {
		return err
	}
	agent, err := a.deps.newAgent(ctx, config)
	if err != nil {
		return fmt.Errorf("initialize agent: %w", err)
	}
	if a.interactive {
		fmt.Fprintln(a.stderr, "Enter prompts or tool-result JSON. Use .help for commands.")
	}
	scanner := bufio.NewScanner(a.stdin)
	scanner.Buffer(make([]byte, 64*1024), maxInputSize)
	for {
		if a.interactive {
			fmt.Fprint(a.stderr, "needlez> ")
		}
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		switch input {
		case ".quit", ".exit":
			return nil
		case ".help":
			fmt.Fprintln(a.stderr, ".reset  rewind the conversation")
			fmt.Fprintln(a.stderr, ".quit   exit needlez")
			continue
		case ".reset":
			if err := agent.Reset(ctx); err != nil {
				return fmt.Errorf("reset: %w", err)
			}
			fmt.Fprintln(a.stderr, "session reset")
			continue
		}
		response, err := agent.Complete(ctx, input, options.maxTokens)
		if err != nil {
			return fmt.Errorf("complete: %w", err)
		}
		if err := writeJSON(a.stdout, response, options.pretty); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	return nil
}

func (a *application) runDoctor(ctx context.Context, args []string) error {
	flags := a.flagSet("Check local engine discovery, loading, and initialization.", "doctor [options]")
	libraryPath := flags.String("library", "", "path to libneedle shared library")
	cacheDir := flags.String("cache", "", "engine cache directory")
	weightsPath := flags.String("weights", "", "path to tuned .cact weights")
	bufferSize := flags.Int("buffer-size", 0, "native response buffer size")
	smoke := flags.Bool("smoke", false, "perform one no-tools completion")
	maxTokens := flags.Int("max-tokens", 32, "smoke-test response token limit")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{errors.New("doctor does not accept positional arguments")}
	}
	platform, err := a.deps.currentPlatform()
	if err != nil {
		fmt.Fprintf(a.stdout, "✗ Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
		return err
	}
	fmt.Fprintf(a.stdout, "✓ Platform: %s\n", platform)
	fmt.Fprintf(a.stdout, "✓ Engine version: %s\n", needle.EngineVersion)

	path, source, err := a.findLibrary(*libraryPath, *cacheDir)
	if err != nil {
		fmt.Fprintln(a.stdout, "✗ Library: not found")
		return fmt.Errorf("%w; run `needlez fetch`", err)
	}
	fmt.Fprintf(a.stdout, "✓ Library: %s (%s)\n", path, source)

	agent, err := a.deps.newAgent(ctx, needle.Config{
		LibraryPath: path,
		WeightsPath: *weightsPath,
		BufferSize:  *bufferSize,
	})
	if err != nil {
		fmt.Fprintln(a.stdout, "✗ Native ABI and initialization")
		return err
	}
	fmt.Fprintln(a.stdout, "✓ Native ABI and initialization")
	if err := agent.Reset(ctx); err != nil {
		fmt.Fprintln(a.stdout, "✗ Reset")
		return err
	}
	fmt.Fprintln(a.stdout, "✓ Reset")
	if !*smoke {
		fmt.Fprintln(a.stdout, "○ Smoke test: skipped (use --smoke)")
		return nil
	}
	response, err := agent.Complete(ctx, "hello", *maxTokens)
	if err != nil {
		fmt.Fprintln(a.stdout, "✗ Smoke test")
		return err
	}
	fmt.Fprintf(
		a.stdout,
		"✓ Smoke test: type=%s prefill=%.1f tok/s decode=%.1f tok/s peak_ram=%.1f MB\n",
		response.Type,
		response.PrefillTPS,
		response.DecodeTPS,
		response.PeakRAMMB,
	)
	return nil
}

func (a *application) findLibrary(configured, cacheDir string) (string, string, error) {
	path, source := configured, "--library"
	if path == "" {
		path, source = os.Getenv(needle.EnvLibraryPath), needle.EnvLibraryPath
	}
	if path == "" {
		cached, err := a.deps.cachedEngine(needle.FetchOptions{CacheDir: cacheDir})
		if err != nil {
			return "", "", err
		}
		return cached, "cache", nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("%s is not a regular file", absolute)
	}
	return absolute, source, nil
}

func (a *application) runVersion(args []string) error {
	flags := a.flagSet("Print version information.", "version")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{errors.New("version does not accept positional arguments")}
	}
	platform, err := a.deps.currentPlatform()
	if err != nil {
		platform = needle.Platform(runtime.GOOS + "-" + runtime.GOARCH + " (unsupported)")
	}
	fmt.Fprintf(a.stdout, "needlez %s\n", effectiveVersion())
	fmt.Fprintf(a.stdout, "engine %s\n", needle.EngineVersion)
	fmt.Fprintf(a.stdout, "platform %s\n", platform)
	return nil
}

func effectiveVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func writeJSON(output io.Writer, value any, pretty bool) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write JSON: %w", err)
	}
	return nil
}

func readFileLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimited(file, limit)
}

func readLimited(input io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("input exceeds %d bytes", limit)
	}
	return data, nil
}
