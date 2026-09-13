package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"yonderllm/internal/config"
)

// newConfigCmd builds the `config` command group: inspect where settings come
// from, and write a starter file.
//
// The group never prints a credential. It reports the *name* of the
// environment variable a provider reads and whether that variable is currently
// set, which is the part a user actually needs when something will not connect.
func newConfigCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and create yonderllm's configuration",
		Long: "Configuration is resolved from four layers, each beating the one\n" +
			"before it: built-in defaults, the TOML file, YONDERLLM_*\n" +
			"environment variables, and command-line flags.\n\n" +
			"These subcommands show the result of that resolution, the file it\n" +
			"came from, and can write a starter file to edit.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(
		newConfigShowCmd(e),
		newConfigPathCmd(e),
		newConfigInitCmd(e),
	)
	return cmd
}

// newConfigShowCmd reports the effective configuration after every layer has
// been applied.
func newConfigShowCmd(e *env) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration",
		Long: "Prints configuration as yonderllm actually sees it, with flags and\n" +
			"environment variables already folded in. API keys are never\n" +
			"printed; only the variable each provider reads, and whether it is\n" +
			"set right now.",
		Example: "  yonderllm config show\n" +
			"  yonderllm config show --json\n" +
			"  yonderllm --provider gemini config show",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := e.resolve()
			if err != nil {
				return err
			}
			if asJSON {
				return writeConfigJSON(e.out, cfg, e.configPath)
			}
			return writeConfigTable(e.out, cfg, e.configPath)
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

// newConfigPathCmd reports where the file lives and whether it exists.
//
// It resolves the path without loading, so it still answers usefully when the
// file exists but fails to parse — which is exactly when it is asked.
func newConfigPathCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "path",
		Short:   "Print the path of the configuration file",
		Long:    "Prints the file yonderllm reads, whether or not it exists.",
		Example: "  yonderllm config path",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := e.settingsPath()
			if err != nil {
				return err
			}
			fmt.Fprintln(e.out, path)
			if !fileExists(path) {
				fmt.Fprintln(e.errOut, "note: this file does not exist yet; run \"yonderllm config init\" to create it")
			}
			return nil
		},
	}
	return cmd
}

// newConfigInitCmd writes a commented starter file.
func newConfigInitCmd(e *env) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter configuration file",
		Long: "Creates the configuration file with the built-in defaults spelled\n" +
			"out and commented, ready to edit. An existing file is left alone\n" +
			"unless --force is given.\n\n" +
			"The file is written readable only by its owner, because it is a\n" +
			"place where an API key may end up.",
		Example: "  yonderllm config init\n" +
			"  yonderllm config init --force\n" +
			"  yonderllm --config ./yonder.toml config init",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := e.settingsPath()
			if err != nil {
				return err
			}
			if fileExists(path) && !force {
				return fmt.Errorf("%s already exists; pass --force to overwrite it", path)
			}
			if dir := filepath.Dir(path); dir != "" && dir != "." {
				// 0o700: the directory may hold a key-bearing file.
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return fmt.Errorf("creating %s: %w", dir, err)
				}
			}
			if err := os.WriteFile(path, []byte(starterConfig()), 0o600); err != nil {
				return fmt.Errorf("writing %s: %w", path, err)
			}
			fmt.Fprintf(e.out, "wrote %s\n", path)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	return cmd
}

// settingsPath resolves the configuration file location, honouring --config.
func (e *env) settingsPath() (string, error) {
	if e.configPath != "" {
		return e.configPath, nil
	}
	return config.Path()
}

// fileExists reports whether path names something that can be stat'd. A stat
// error other than "not exist" is treated as existing, so init errs towards
// refusing to clobber rather than towards writing.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil || !os.IsNotExist(err)
}

// writeConfigTable renders the resolved configuration as two blocks: the
// session-wide settings, then one row per provider.
func writeConfigTable(w io.Writer, cfg config.Config, override string) error {
	path, source := configSource(override)
	fmt.Fprintf(w, "config file  %s (%s)\n", path, source)
	fmt.Fprintf(w, "provider     %s\n", cfg.Provider)
	fmt.Fprintf(w, "model        %s\n", orDash(cfg.Providers[cfg.Provider].Model))
	fmt.Fprintf(w, "mode         %s\n", cfg.Mode)
	fmt.Fprintf(w, "max tokens   %d\n", cfg.MaxTokens)
	fmt.Fprintf(w, "daily cap    %s\n", capLabel(cfg.DailyCap))
	fmt.Fprintf(w, "fallbacks    %s\n", orDash(strings.Join(cfg.Fallbacks, ", ")))

	if len(cfg.Providers) == 0 {
		fmt.Fprintln(w, "\nNo providers are configured.")
		return nil
	}

	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tMODEL\tKEY\tSTATUS\tBASE URL")
	for _, name := range sortedProviderNames(cfg) {
		pc := cfg.Providers[name]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			name,
			orDash(pc.Model),
			orDash(envLabel(pc.APIKeyEnv)),
			statusLabel(cfg.Credentialed(name)),
			orDash(pc.BaseURL),
		)
	}
	return tw.Flush()
}

// writeConfigJSON emits the same information as writeConfigTable.
func writeConfigJSON(w io.Writer, cfg config.Config, override string) error {
	path, source := configSource(override)

	providers := make([]wireConfigProvider, 0, len(cfg.Providers))
	for _, name := range sortedProviderNames(cfg) {
		pc := cfg.Providers[name]
		providers = append(providers, wireConfigProvider{
			Name:      name,
			Model:     pc.Model,
			BaseURL:   pc.BaseURL,
			APIKeyEnv: pc.APIKeyEnv,
			Ready:     cfg.Credentialed(name),
		})
	}

	fallbacks := cfg.Fallbacks
	if fallbacks == nil {
		fallbacks = []string{}
	}

	out := wireConfig{
		ConfigFile:   path,
		ConfigSource: source,
		Provider:     cfg.Provider,
		Model:        cfg.Providers[cfg.Provider].Model,
		Mode:         cfg.Mode,
		MaxTokens:    cfg.MaxTokens,
		DailyCap:     cfg.DailyCap,
		Fallbacks:    fallbacks,
		Providers:    providers,
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// wireConfig is the JSON shape of `config show --json`. It is declared here
// rather than reused from config.Config so the on-screen contract can stay
// stable while the internal struct evolves — and so no unexported key field is
// ever within reach of the encoder.
type wireConfig struct {
	ConfigFile   string               `json:"config_file"`
	ConfigSource string               `json:"config_source"`
	Provider     string               `json:"provider"`
	Model        string               `json:"model,omitempty"`
	Mode         string               `json:"mode"`
	MaxTokens    int                  `json:"max_tokens"`
	DailyCap     int                  `json:"daily_cap"`
	Fallbacks    []string             `json:"fallbacks"`
	Providers    []wireConfigProvider `json:"providers"`
}

type wireConfigProvider struct {
	Name      string `json:"name"`
	Model     string `json:"model,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	Ready     bool   `json:"ready"`
}

// configSource resolves the path to report and labels where it came from, so
// that `show` explains which file it read rather than only naming one.
func configSource(override string) (path, source string) {
	if override != "" {
		return override, "--config"
	}
	p, err := config.Path()
	if err != nil {
		return "", "unavailable"
	}
	if !fileExists(p) {
		return p, "not found, using defaults"
	}
	if os.Getenv("YONDERLLM_CONFIG") != "" {
		return p, "YONDERLLM_CONFIG"
	}
	return p, "default location"
}

// sortedProviderNames lists configured providers with the active one first,
// then the fallback chain in order, then the rest alphabetically. It mirrors
// the ordering of the providers subcommand so the two read alike.
func sortedProviderNames(cfg config.Config) []string {
	seen := make(map[string]bool, len(cfg.Providers))
	names := make([]string, 0, len(cfg.Providers))

	for _, name := range cfg.Chain() {
		if _, ok := cfg.Providers[name]; ok && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}

	rest := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(names, rest...)
}

// capLabel spells out the disabled case, which "0" alone would not.
func capLabel(cap int) string {
	if cap <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d requests/day", cap)
}

// envLabel renders an environment variable name for display, marking the
// providers that need no credential at all.
func envLabel(name string) string {
	if name == "" {
		return "none needed"
	}
	return name
}

// starterConfig is the commented file written by `config init`. It is a
// literal rather than a marshalled Config because the point of the file is the
// comments: a TOML encoder would emit the values and drop every explanation.
func starterConfig() string {
	return fmt.Sprintf(`# yonderllm configuration
#
# Every value here is optional. Anything you leave out falls back to the
# built-in default, and anything you set here is still overridden by a
# YONDERLLM_* environment variable or a command-line flag.
#
# This file never holds an API key. Each provider names the environment
# variable its key is read from instead.

# Provider tried first.
provider = %q

# Tried in order when the active provider reports a quota or auth failure.
fallbacks = ["gemini", "openrouter"]

# Permission mode at startup: "chat" (no filesystem or shell), "code"
# (read and search, patches need approval), or "agent" (read, search,
# write, and execute).
mode = %q

# Cap on output tokens per request.
max_tokens = %d

# Cap on requests per day, to protect a free tier. Set to 0 for no cap.
daily_cap = %d

[providers.groq]
base_url    = "https://api.groq.com/openai/v1"
api_key_env = "GROQ_API_KEY"
# model     = "llama-3.3-70b-versatile"

[providers.gemini]
base_url    = "https://generativelanguage.googleapis.com/v1beta/openai"
api_key_env = "GEMINI_API_KEY"
# model     = "gemini-2.0-flash"

[providers.openrouter]
base_url    = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"
# model     = "meta-llama/llama-3.3-70b-instruct:free"

[providers.surplus]
base_url    = "https://api.surplusintelligence.ai/v1"
api_key_env = "SURPLUS_API_KEY"
model       = "gpt-5.6-sol"

# A local or self-hosted OpenAI-compatible server needs no key:
# [providers.local]
# base_url = "http://localhost:8080/v1"
# model    = "whatever-it-serves"
`,
		config.DefaultProvider,
		config.DefaultMode,
		config.DefaultMaxTokens,
		config.DefaultDailyCap,
	)
}
