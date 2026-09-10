package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// This file supplies yonderllm's own help and completion commands.
//
// Cobra generates both automatically, and both are fine — but their wording is
// cobra's rather than ours: "Help about any command", "Generate the
// autocompletion script for the specified shell", title case in a tree that is
// otherwise sentence case. Since the help templates are already replaced in
// help.go, leaving these two commands in cobra's voice would make the seam
// visible on the very first page a new user reads.
//
// Both constructors are registered in newRoot before applyTemplates runs.
// Cobra's InitDefaultHelpCmd and InitDefaultCompletionCmd each check for an
// existing command first, so registering early both installs our wording and
// suppresses the generated pair.

// newHelpCmd builds the `help` command.
//
// It takes no environment: help reads nothing, resolves no configuration, and
// writes only through the command it was asked about, so there is nothing for
// an *env to carry.
func newHelpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "Show help for a command",
		Long: "Show help for yonderllm or for one of its commands.\n\n" +
			"Equivalent to passing --help to the command itself; it exists because\n" +
			"\"yonderllm help ask\" is the first thing many people try.",
		Example: `yonderllm help
yonderllm help ask
yonderllm help config init`,
		Args: cobra.ArbitraryArgs,
		// Completing the argument means "help con<tab>" offers "config",
		// which is the case where a help command earns its keep.
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			target, _, err := cmd.Root().Find(args)
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			if target == nil {
				target = cmd.Root()
			}

			var names []string
			for _, sub := range target.Commands() {
				if !sub.IsAvailableCommand() && sub.Name() != "help" {
					continue
				}
				if strings.HasPrefix(sub.Name(), toComplete) {
					names = append(names, cobra.CompletionWithDesc(sub.Name(), sub.Short))
				}
			}
			return names, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			target, _, err := cmd.Root().Find(args)
			if err != nil || target == nil {
				// Cobra would print its own "Unknown help topic" line and
				// the root usage page here. Returning instead routes the
				// failure through Execute, so a bad help topic reports and
				// exits exactly like every other usage mistake.
				return fmt.Errorf("unknown help topic %q", strings.Join(args, " "))
			}

			// Find returns a command that has not been through Execute, so
			// it carries no context and, if it was never reached by the
			// template walk, no default flags. Priming all three keeps the
			// page it prints identical to `<command> --help`.
			target.SetContext(cmd.Context())
			target.InitDefaultHelpFlag()
			target.InitDefaultVersionFlag()
			return target.Help()
		},
	}
}

// newCompletionCmd builds the `completion` command group.
//
// The generators are cobra's — reimplementing shell completion would be
// gratuitous — but the command wrapping them is ours, so the help page reads in
// the same voice as the rest of the tree and each shell carries the install
// line for that shell rather than a generic one.
func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion",
		Short: "Generate a shell completion script",
		Long: "Print a completion script for the given shell to standard output.\n\n" +
			"Completion covers subcommands, flags, and the arguments yonderllm can\n" +
			"enumerate — provider names for --provider, model ids for --model — so\n" +
			"it saves more typing than the command list alone would suggest.\n\n" +
			"Each subcommand's help explains where to install its output.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(
		newCompletionShellCmd(
			"bash",
			"Generate the bash completion script",
			"Print a bash completion script.\n\n"+
				"Requires bash-completion; on most systems it is already installed.\n"+
				"Load it for the current shell, or install it for every session:\n\n"+
				"  source <(yonderllm completion bash)\n"+
				"  yonderllm completion bash > /etc/bash_completion.d/yonderllm",
			func(w io.Writer, includeDesc bool) error {
				return cmd.Root().GenBashCompletionV2(w, includeDesc)
			},
		),
		newCompletionShellCmd(
			"zsh",
			"Generate the zsh completion script",
			"Print a zsh completion script.\n\n"+
				"Completion must be enabled in the shell first — add \"autoload -U\n"+
				"compinit; compinit\" to ~/.zshrc — then drop the script anywhere on\n"+
				"$fpath, where zsh will find it on the next login:\n\n"+
				"  yonderllm completion zsh > \"${fpath[1]}/_yonderllm\"",
			func(w io.Writer, includeDesc bool) error {
				if !includeDesc {
					return cmd.Root().GenZshCompletionNoDesc(w)
				}
				return cmd.Root().GenZshCompletion(w)
			},
		),
		newCompletionShellCmd(
			"fish",
			"Generate the fish completion script",
			"Print a fish completion script.\n\n"+
				"Load it for the current shell, or install it for every session:\n\n"+
				"  yonderllm completion fish | source\n"+
				"  yonderllm completion fish > ~/.config/fish/completions/yonderllm.fish",
			func(w io.Writer, includeDesc bool) error {
				return cmd.Root().GenFishCompletion(w, includeDesc)
			},
		),
		newCompletionShellCmd(
			"powershell",
			"Generate the PowerShell completion script",
			"Print a PowerShell completion script.\n\n"+
				"Load it for the current session, or append it to your profile so it\n"+
				"loads on every new one:\n\n"+
				"  yonderllm completion powershell | Out-String | Invoke-Expression\n"+
				"  yonderllm completion powershell >> $PROFILE",
			func(w io.Writer, includeDesc bool) error {
				if !includeDesc {
					return cmd.Root().GenPowerShellCompletion(w)
				}
				return cmd.Root().GenPowerShellCompletionWithDesc(w)
			},
		),
	)

	return cmd
}

// newCompletionShellCmd builds one shell's subcommand.
//
// The four generators differ in signature — two take an includeDesc bool, two
// come as separate With/NoDesc functions — so callers pass an adapter with a
// single shape and the flag, the argument rules, and the writer are handled
// once here instead of four times.
func newCompletionShellCmd(use, short, long string, gen func(w io.Writer, includeDesc bool) error) *cobra.Command {
	var noDescriptions bool

	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  cobra.NoArgs,
		// The script is the output; a "[flags]" suffix in the usage line
		// would suggest the command does something else as well.
		DisableFlagsInUseLine: true,
		// A completion script is data, not a command tree to explore, so it
		// should never be offered as a target for shell completion itself.
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return gen(cmd.OutOrStdout(), !noDescriptions)
		},
	}

	cmd.Flags().BoolVar(&noDescriptions, "no-descriptions", false, "omit the description shown beside each completion")
	return cmd
}
