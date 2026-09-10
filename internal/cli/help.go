package cli

import (
	"strings"
	"text/template"

	"github.com/spf13/cobra"
)

// This file replaces cobra's help, usage, and version output wholesale.
//
// Cobra is kept for what it is genuinely good at — subcommand routing, flag
// parsing, "did you mean" suggestions, shell completion — but its default help
// layout is a compromise shared by thousands of tools. yonderllm prints its
// own: sections in a fixed order, two-space indentation throughout, examples
// where they help, and no trailing "Use ... --help for more information about a
// command" line on a page that already lists the commands.

// usageTemplate is the body printed for --help and for usage errors.
//
// The template is written against cobra's own data model, so aliases, flag
// groups, and inherited flags stay correct as commands are added.
const usageTemplate = `{{with (or .Long .Short)}}{{. | trimTrailingWhitespaces}}

{{end}}Usage
  {{.UseLine}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} <command> [flags]{{end}}
{{- if gt (len .Aliases) 0}}

Aliases
  {{.NameAndAliases}}
{{- end}}
{{- if .HasExample}}

Examples
{{indent .Example 2}}
{{- end}}
{{- if .HasAvailableSubCommands}}

Commands
{{- range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name (padWidth $)}}{{.Short}}{{end}}{{end}}
{{- end}}
{{- if .HasAvailableLocalFlags}}

Flags
{{indent .LocalFlags.FlagUsages 2}}
{{- end}}
{{- if .HasAvailableInheritedFlags}}

Global flags
{{indent .InheritedFlags.FlagUsages 2}}
{{- end}}
{{- if .HasAvailableSubCommands}}

Run "{{.CommandPath}} <command> --help" for detail on a command.
{{- end}}
`

// helpTemplate defers entirely to the usage body. Cobra keeps help and usage
// separate so that a usage error can print a terser form; yonderllm prints the
// same page in both cases, because a user who just got a flag wrong is exactly
// the user who needs the full list of flags.
//
// The description lives in usageTemplate alone. Cobra's stock helpTemplate
// prints it and then interpolates .UsageString, which is how the default layout
// ends up repeating a long description twice on any command that has one.
const helpTemplate = `{{.UsageString}}`

// versionTemplate keeps the version line to one machine-greppable string.
const versionTemplate = `{{.Name}} {{.Version}}
`

// applyTemplates installs yonderllm's output shape on cmd and, recursively, on
// every command below it. Cobra propagates templates to children only when they
// have none of their own, which is fragile once a subcommand sets anything, so
// the walk here is explicit.
func applyTemplates(cmd *cobra.Command) {
	// trimTrailingWhitespaces is also registered by cobra itself, but its name
	// is not part of cobra's documented API; registering our own copy means a
	// rename upstream cannot turn a help page into a runtime panic.
	cobra.AddTemplateFuncs(template.FuncMap{
		"indent":                  indent,
		"padWidth":                padWidth,
		"trimTrailingWhitespaces": trimTrailingWhitespaces,
	})

	cmd.SetUsageTemplate(usageTemplate)
	cmd.SetHelpTemplate(helpTemplate)
	cmd.SetVersionTemplate(versionTemplate)

	// Cobra's stock help flag description is capitalised and mentions the
	// command by name; ours matches the sentence case of every other flag.
	cmd.InitDefaultHelpFlag()
	if f := cmd.Flags().Lookup("help"); f != nil {
		f.Usage = "show help for this command"
	}

	// Likewise for --version, which cobra otherwise describes as "version for
	// yonderllm" — a phrase that reads as a noun in a column of verbs.
	cmd.InitDefaultVersionFlag()
	if f := cmd.Flags().Lookup("version"); f != nil {
		f.Usage = "print the version and exit"
	}

	for _, child := range cmd.Commands() {
		applyTemplates(child)
	}
}

// indent re-indents every non-empty line of s to start at column n.
//
// It dedents by the smallest leading-space run in the block before applying the
// padding, rather than stripping each line's own leading space. That matters
// because pflag's FlagUsages already indents two spaces and pads flags without
// a shorthand by four more so that "--config" lines up under "-m, --model";
// trimming each line independently would collapse that column and leave the
// flag list ragged. Any relative indentation the caller built — pflag's
// shorthand gutter, wrapped usage continuation lines, hanging examples — is
// preserved as-is.
func indent(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")

	common := -1
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "" {
			continue
		}
		lead := len(trimmed) - len(strings.TrimLeft(trimmed, " "))
		if common < 0 || lead < common {
			common = lead
		}
	}
	if common < 0 {
		common = 0
	}

	pad := strings.Repeat(" ", n)
	for i, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			lines[i] = ""
			continue
		}
		lines[i] = pad + line[common:]
	}
	return strings.Join(lines, "\n")
}

// trimTrailingWhitespaces strips trailing spaces, tabs, and newlines. Cobra
// registers a function of the same name, but relying on it couples our
// templates to an undocumented internal; templates are parsed at runtime, so a
// rename upstream would surface as a panic on the first help page rather than a
// build failure.
func trimTrailingWhitespaces(s string) string {
	return strings.TrimRight(s, " \t\r\n")
}

// padWidth is the column at which command descriptions start: the longest
// command name plus a two-space gutter. Computing it from the actual commands
// keeps the list aligned as commands are added or renamed.
func padWidth(cmd *cobra.Command) int {
	width := 0
	for _, c := range cmd.Commands() {
		if !c.IsAvailableCommand() && c.Name() != "help" {
			continue
		}
		if n := len(c.Name()); n > width {
			width = n
		}
	}
	return width + 2
}
