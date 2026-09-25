// Versioned conversation snapshots live beside configuration, outside the
// disposable cache. Saving redacts recognized credentials on a copy; it is
// best-effort redaction, not encryption or a guarantee against every secret.
package session

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MoneyPack/yonderllm/internal/provider"
	"github.com/MoneyPack/yonderllm/internal/redact"
)

// sessionsVersion is the on-disk schema version. Bumping it lets a future
// reader refuse (or migrate) files this version could not understand instead
// of silently misreading a conversation.
const sessionsVersion = 1

// maxSessionBytes bounds one saved conversation on disk, in both directions.
// A transcript that large is almost certainly a tool result gone wrong, and
// reading one back without a limit would let a corrupt file exhaust memory.
const maxSessionBytes = 16 << 20

// Each new/resumed interactive run gets its own autosave. Named snapshots
// remain untouched unless explicitly saved to that name again.
func (s *Session) EnableSaving(store *Sessions, name string) error {
	if name == "" {
		name = fmt.Sprintf("session-%x", randomSessionID())
	}
	if err := validSessionName(name); err != nil {
		return err
	}
	s.store, s.saveName = store, name
	return nil
}

func randomSessionID() []byte {
	id := make([]byte, 16)
	_, err := rand.Read(id)
	if err != nil {
		panic(err)
	}
	return id
}

// DefaultSessionsDir returns the directory that holds saved conversations,
// creating it if needed. Exported so the CLI package can locate the store on
// the same path the TUI's /save uses.
func DefaultSessionsDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "yonderllm", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create sessions directory: %w", err)
	}
	return dir, nil
}

// DefaultSessions returns a store rooted at the default directory, creating
// it if needed.
func DefaultSessions() (*Sessions, error) {
	dir, err := DefaultSessionsDir()
	if err != nil {
		return nil, err
	}
	return OpenSessions(dir)
}

// SavedConversation is one named conversation as it is stored and restored.
//
// Save redacts its content before serialization; callers may supply raw history.
type SavedConversation struct {
	Updated  time.Time
	Name     string
	Provider string
	Model    string
	System   string
	// Messages is the ordered turn list, with secrets removed.
	Messages []provider.Message
}

// apiConversation is the wire/disk form of a saved conversation. It is a
// separate type from SavedConversation so the file layout can be versioned
// and tightened without changing the in-memory shape.
type apiConversation struct {
	Updated  time.Time    `json:"updated"`
	Version  int          `json:"version"`
	Name     string       `json:"name"`
	Provider string       `json:"provider"`
	Model    string       `json:"model"`
	System   string       `json:"system,omitempty"`
	Messages []apiMessage `json:"messages"`
}

type apiMessage struct {
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	ToolCalls  []apiCall `json:"tool_calls,omitempty"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
}

type apiCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Sessions is a directory of saved conversations.
type Sessions struct {
	dir string
}

// OpenSessions returns a store rooted at dir, creating it when needed.
func OpenSessions(dir string) (*Sessions, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create sessions directory: %w", err)
	}
	return &Sessions{dir: dir}, nil
}

// sanitizeName turns a user-supplied conversation name into a safe filename,
// stripping anything that could escape the sessions directory or hide a file.
func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), ".")
}

func (s *Sessions) pathFor(name string) string {
	return filepath.Join(s.dir, sanitizeName(name)+".json")
}

// Save writes a conversation atomically, redacting every message first. Saving
// a name that is already in use replaces the earlier conversation.
func (s *Sessions) Save(name string, conv SavedConversation) error {
	if err := validSessionName(name); err != nil {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := validateMessages(conv.Messages); err != nil {
		return err
	}

	out := apiConversation{
		Version:  sessionsVersion,
		Updated:  time.Now().UTC(),
		Name:     name,
		Provider: conv.Provider,
		Model:    conv.Model,
		System:   redact.Text(conv.System),
		Messages: toAPI(redactSavedMessages(conv.Messages)),
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	if len(data) > maxSessionBytes {
		return errors.New("session exceeds 16 MiB")
	}

	tmp, err := os.CreateTemp(s.dir, ".session-*.json")
	if err != nil {
		return fmt.Errorf("create session file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write session: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync session: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close session: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("protect session: %w", err)
	}
	if err := os.Rename(tmpName, s.pathFor(name)); err != nil {
		return fmt.Errorf("install session: %w", err)
	}
	return nil
}

// Get loads a conversation by its validated name.
func (s *Sessions) Get(name string) (SavedConversation, error) {
	if err := validSessionName(name); err != nil {
		return SavedConversation{}, err
	}
	conv, err := s.loadFile(s.pathFor(name))
	if err != nil {
		return SavedConversation{}, err
	}
	return conv, nil
}

// List returns valid snapshots newest first by save time. Temporary files and
// unsupported or corrupt snapshots are excluded.
func (s *Sessions) List() ([]SavedConversation, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}
	var out []SavedConversation
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		conv, err := s.loadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			// A single corrupt conversation should not hide the rest.
			continue
		}
		out = append(out, conv)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Updated.Equal(out[j].Updated) {
			return out[i].Name > out[j].Name
		}
		return out[i].Updated.After(out[j].Updated)
	})
	return out, nil
}

// Delete removes a saved conversation. It reports an error if no such
// conversation exists.
func (s *Sessions) Delete(name string) error {
	if err := validSessionName(name); err != nil {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	path := s.pathFor(name)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no saved conversation %q", name)
		}
		return fmt.Errorf("stat session: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove session: %w", err)
	}
	return nil
}

func (s *Sessions) loadFile(path string) (SavedConversation, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SavedConversation{}, fmt.Errorf("no saved conversation at %q", filepath.Base(path))
		}
		return SavedConversation{}, fmt.Errorf("read session: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSessionBytes+1))
	if err != nil {
		return SavedConversation{}, err
	}
	if len(data) > maxSessionBytes {
		return SavedConversation{}, errors.New("session exceeds 16 MiB")
	}
	var out apiConversation
	if err := json.Unmarshal(data, &out); err != nil {
		return SavedConversation{}, fmt.Errorf("decode session: %w", err)
	}
	if out.Version != sessionsVersion {
		return SavedConversation{}, fmt.Errorf("unsupported session version %d", out.Version)
	}
	if out.Name != strings.TrimSuffix(filepath.Base(path), ".json") {
		return SavedConversation{}, errors.New("session name does not match file")
	}
	if err := validateMessages(fromAPI(out.Messages)); err != nil {
		return SavedConversation{}, err
	}
	return SavedConversation{
		Updated:  out.Updated,
		Name:     out.Name,
		Provider: out.Provider,
		Model:    out.Model,
		System:   out.System,
		Messages: fromAPI(out.Messages),
	}, nil
}

func validSessionName(name string) error {
	if name == "" || len(name) > 80 || name != strings.ToLower(name) || sanitizeName(name) != name || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return errors.New("session name must be 1-80 lowercase letters, digits, dots, hyphens or underscores, with no leading/trailing dot")
	}
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
		return errors.New("reserved session name")
	}
	return nil
}

// lock serialises writers to the store. Readers do not take it: a snapshot is
// installed by rename, so a reader sees either the old file or the new one.
func (s *Sessions) lock() (func(), error) {
	unlock, err := lockFile(filepath.Join(s.dir, ".store.lock"))
	if err != nil {
		return nil, fmt.Errorf("lock sessions: %w", err)
	}
	return unlock, nil
}

func (s *Sessions) Latest() (SavedConversation, error) {
	list, err := s.List()
	if err != nil {
		return SavedConversation{}, err
	}
	if len(list) == 0 {
		return SavedConversation{}, errors.New("no saved conversations")
	}
	return list[0], nil
}

// Reject incomplete tool exchanges rather than replaying saved actions.
func validateMessages(msgs []provider.Message) error {
	pending := map[string]bool{}
	for _, m := range msgs {
		if m.Role == provider.RoleTool {
			if !pending[m.ToolCallID] || len(m.ToolCalls) > 0 {
				return errors.New("unmatched saved tool result")
			}
			delete(pending, m.ToolCallID)
			continue
		}
		if len(pending) > 0 {
			return errors.New("incomplete saved tool exchange")
		}
		if m.Role != provider.RoleUser && m.Role != provider.RoleAssistant {
			return errors.New("invalid saved message role")
		}
		if m.ToolCallID != "" || (m.Role != provider.RoleAssistant && len(m.ToolCalls) > 0) {
			return errors.New("invalid saved tool message")
		}
		for _, c := range m.ToolCalls {
			if c.ID == "" || c.Name == "" || pending[c.ID] {
				return errors.New("invalid saved tool call")
			}
			pending[c.ID] = true
		}
	}
	if len(pending) > 0 {
		return errors.New("incomplete saved tool exchange")
	}
	return nil
}

// Structured arguments need key-aware redaction as well as prose redaction.
func redactSavedMessages(msgs []provider.Message) []provider.Message {
	out := redactOutbound(msgs)
	for i := range out {
		for j := range out[i].ToolCalls {
			c := &out[i].ToolCalls[j]
			var value any
			if json.Unmarshal([]byte(c.Arguments), &value) == nil {
				value = redactJSON(value)
				data, _ := json.Marshal(value)
				c.Arguments = string(data)
			} else {
				// Arguments that are not JSON cannot be inspected key by
				// key, and a blob the model produced is not worth keeping
				// at the risk of a credential inside it reaching disk.
				c.Arguments = redact.Placeholder
			}
		}
	}
	return out
}

func redactJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if redact.NamesSecret(k) {
				x[k] = redact.Placeholder
			} else {
				x[k] = redactJSON(val)
			}
		}
		return x
	case []any:
		for i, val := range x {
			x[i] = redactJSON(val)
		}
		return x
	case string:
		return redact.Text(x)
	default:
		return v
	}
}

func toAPI(msgs []provider.Message) []apiMessage {
	out := make([]apiMessage, 0, len(msgs))
	for _, m := range msgs {
		am := apiMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		for _, c := range m.ToolCalls {
			am.ToolCalls = append(am.ToolCalls, apiCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
		}
		out = append(out, am)
	}
	return out
}

func fromAPI(msgs []apiMessage) []provider.Message {
	out := make([]provider.Message, 0, len(msgs))
	for _, am := range msgs {
		m := provider.Message{Role: provider.Role(am.Role), Content: am.Content, ToolCallID: am.ToolCallID}
		for _, c := range am.ToolCalls {
			m.ToolCalls = append(m.ToolCalls, provider.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
		}
		out = append(out, m)
	}
	return out
}
