package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/MoneyPack/yonderllm/internal/terminaltext"

	"github.com/MoneyPack/yonderllm/internal/config"
)

// newProvidersCmd builds the command that reports what backends are configured
// and which of them are usable.
//
// This is the command a user reaches for when a request failed and they do not
// yet know whether the cause is a missing key, a provider that was never
// configured, or a fallback chain that is empty. It therefore answers entirely
// from configuration and never opens a connection: it must keep working when
// every provider is down.
func newProvidersCmd(e *env) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "providers",
		Short: "Show the configured providers and their credentials",
		Long: "List every configured provider, in the order the fallback chain tries\n" +
			"them, and report whether each one has a usable API key.\n\n" +
			"Keys themselves are never shown. What is shown is the name of the\n" +
			"environment variable a provider reads, which is the thing you need in\n" +
			"order to fix a missing credential.",
		Example: `yonderllm providers
yonderllm providers --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := e.resolve()
			if err != nil {
				return err
			}

			rows := providerRows(cfg)
			if asJSON {
				return writeProvidersJSON(cmd, rows)
			}
			return writeProvidersTable(cmd, rows)
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the provider list as a JSON object")
	return cmd
}

// providerRow is one line of the report, already reduced to what is printable.
// Deriving it once means the table and the JSON writer cannot disagree about
// what "ready" means.
type providerRow struct {
	Name string
	// Role is active, fallback, or configured: the position a provider
	// holds in the chain, or none at all.
	Role string
	// Position is the provider's 1-based place in the chain, or 0 when it
	// is configured but unreachable by fallback.
	Position int
	Model    string
	KeyEnv   string
	Ready    bool
	BaseURL  string
}

// providerRows orders providers by their place in the fallback chain, then
// lists any leftovers alphabetically.
//
// Chain order is the order that matters: it is the sequence a failing request
// will actually walk. Providers outside the chain are still worth printing,
// because a configured-but-unused provider is usually a typo in fallbacks.
func providerRows(cfg config.Config) []providerRow {
	chain := cfg.Chain()
	inChain := make(map[string]bool, len(chain))

	rows := make([]providerRow, 0, len(cfg.Providers))
	for i, name := range chain {
		if _, ok := cfg.Providers[name]; !ok {
			// Validate rejects this, so reaching it would mean the
			// config was mutated after loading. Skip rather than
			// print a row of empty columns.
			continue
		}
		inChain[name] = true
		role := "fallback"
		if i == 0 {
			role = "active"
		}
		rows = append(rows, newProviderRow(cfg, name, role, i+1))
	}

	rest := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		if !inChain[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	for _, name := range rest {
		rows = append(rows, newProviderRow(cfg, name, "configured", 0))
	}
	return rows
}

// newProviderRow reduces one entry of cfg.Providers to a printable row.
func newProviderRow(cfg config.Config, name, role string, position int) providerRow {
	pc := cfg.Providers[name]
	return providerRow{
		Name:     name,
		Role:     role,
		Position: position,
		Model:    pc.Model,
		KeyEnv:   pc.APIKeyEnv,
		// Credentialed, not apiKey != "", because a local server needs
		// no key and is ready without one.
		Ready:   cfg.Credentialed(name),
		BaseURL: pc.BaseURL,
	}
}

// writeProvidersTable renders the report for a human reader.
func writeProvidersTable(cmd *cobra.Command, rows []providerRow) error {
	out := cmd.OutOrStdout()

	if len(rows) == 0 {
		fmt.Fprintln(out, "No providers are configured.")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "  PROVIDER\tROLE\tMODEL\tKEY\tSTATUS")
	missing := 0
	for _, r := range rows {
		marker := " "
		if r.Role == "active" {
			marker = "*"
		}
		if !r.Ready {
			missing++
		}
		fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\t%s\n",
			marker, r.Name, r.Role, orDash(r.Model), keyLabel(r), statusLabel(r.Ready))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if missing > 0 {
		// The fix is always the same shape — export a variable — so say
		// so once rather than repeating it on every unready row.
		fmt.Fprintf(out, "\nSet the listed environment variable to enable a provider.\n")
	}
	return nil
}

// wireProvider is the JSON shape of one provider entry, declared separately
// from [config.ProviderConfig] so the published contract does not move when an
// internal field is renamed — and so no field can accidentally carry a key.
type wireProvider struct {
	Name       string `json:"name"`
	Role       string `json:"role"`
	Position   int    `json:"position,omitempty"`
	Model      string `json:"model,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`
	APIKeyEnv  string `json:"api_key_env,omitempty"`
	Ready      bool   `json:"ready"`
	Credential string `json:"credential"`
}

// wireProviderList is the top-level document written by providers --json.
type wireProviderList struct {
	Providers []wireProvider `json:"providers"`
}

// writeProvidersJSON renders the report as a single indented object.
func writeProvidersJSON(cmd *cobra.Command, rows []providerRow) error {
	doc := wireProviderList{Providers: make([]wireProvider, 0, len(rows))}
	for _, r := range rows {
		credential := "missing"
		switch {
		case r.KeyEnv == "":
			// No variable named at all: the provider is reached
			// without a credential, which is a different state from
			// having one and not finding it.
			credential = "not required"
		case r.Ready:
			credential = "present"
		}
		doc.Providers = append(doc.Providers, wireProvider{
			Name:       r.Name,
			Role:       r.Role,
			Position:   r.Position,
			Model:      r.Model,
			BaseURL:    r.BaseURL,
			APIKeyEnv:  r.KeyEnv,
			Ready:      r.Ready,
			Credential: credential,
		})
	}

	enc := json.NewEncoder(terminaltext.Raw(cmd.OutOrStdout()))
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// keyLabel names the environment variable a provider reads, or says that it
// needs none. It never renders the key.
func keyLabel(r providerRow) string {
	if r.KeyEnv == "" {
		return "none needed"
	}
	return r.KeyEnv
}

// statusLabel reports whether a provider can be used right now.
func statusLabel(ready bool) string {
	if ready {
		return "ready"
	}
	return "no key"
}

// orDash renders an unset field as a dash so a column is never blank, which
// would otherwise read as a formatting bug rather than as absent data.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
