// Package redact removes text that looks like a credential before it leaves
// the machine or lands on disk.
//
// Two callers need this and they need different strengths of it. Conversation
// content travels to a model and is saved in transcripts, so [Text] is
// conservative: it removes only what is unmistakably a secret and passes
// everything else through byte for byte. Provider error messages are short
// sentences from an API that sometimes echo the offending key back, so
// [Message] is aggressive and also strips any long opaque run of characters.
// Both share the same vendor prefixes and the same idea of a key character,
// which is the point of keeping them in one package: a new prefix is added
// once and both paths learn it.
//
// This is best-effort redaction, not a guarantee against every secret.
package redact

import "strings"

// Placeholder is what a removed credential leaves behind. It is deliberately
// visible: a model that receives it can say "you redacted that" instead of
// silently reasoning about a value that is no longer there.
const Placeholder = "[redacted]"

// keyPrefixes are the vendor prefixes a bare API key is recognised by. The
// list is short on purpose: every entry here is a token that will be removed
// from a user's prompt, so each one has to be a strong signal on its own.
var keyPrefixes = []string{"sk-", "sk_", "gsk_", "AIza", "or-"}

// bearerMarker introduces a credential in an Authorization header, which is
// the most common way one arrives in a prompt: pasted along with a curl
// command the user wants explained.
const bearerMarker = "bearer "

// minKeyLen is the shortest token a vendor prefix will be trusted on. A prefix
// alone is not enough: "sk-" also begins plenty of ordinary words once a
// hyphen is involved. Requiring the length a real key has keeps the rule from
// firing on prose that merely mentions one.
const minKeyLen = 16

// minOpaqueLen is the shortest bare run of key characters [Message] treats as
// a credential even without a vendor prefix. It is long enough that a word or
// a version number never reaches it.
const minOpaqueLen = 24

// Text removes obvious secrets from conversation content.
//
// Three shapes are recognised, each a strong signal on its own: a value
// following a bearer marker, a token carrying a known vendor key prefix, and
// the right-hand side of an assignment whose name says it holds a credential.
// Everything else is passed through byte for byte, including runs of
// whitespace: this text is going to a model, and collapsing the layout of a
// pasted file would cost more in comprehension than it saves.
//
// Deliberately absent is the "long opaque run of characters" rule [Message]
// applies. That rule is safe on a short sentence from an API but not on
// conversation content, where it would swallow commit hashes, UUIDs and any
// sufficiently long identifier. Guessing wrong in that direction corrupts the
// question being asked, so obvious secrets are caught and ambiguous ones are
// left alone.
func Text(s string) string {
	if s == "" {
		return s
	}

	var b strings.Builder
	written := 0
	replace := func(start, end int, value string) {
		if b.Cap() == 0 {
			b.Grow(len(s))
		}
		b.WriteString(s[written:start])
		b.WriteString(value)
		written = end
	}

	// Set once an assignment has named a secret but its value has not been
	// reached yet, which happens when the value is quoted: KEY="..." puts a
	// delimiter between the name and the thing to remove.
	valueIsSecret := false

	for i := 0; i < len(s); {
		if n := bearerAt(s, i); n > 0 {
			end := endOfToken(s, i+n)
			replace(i+n, end, Placeholder)
			i = end
			valueIsSecret = false
			continue
		}

		if isTokenEnd(s[i]) {
			// Only a quote may stand between a secret's name and its value.
			// Anything else means the value never arrived.
			if !isQuote(s[i]) {
				valueIsSecret = false
			}
			i++
			continue
		}

		end := endOfToken(s, i)
		if valueIsSecret {
			replace(i, end, Placeholder)
			valueIsSecret = false
		} else {
			text, expectValue := redactToken(s[i:end])
			if text != s[i:end] {
				replace(i, end, text)
			}
			valueIsSecret = expectValue
		}
		i = end
	}

	if written == 0 {
		return s
	}
	b.WriteString(s[written:])
	return b.String()
}

// Message removes anything shaped like a credential from a short piece of
// provider text, such as the message in an error envelope.
//
// It is the aggressive counterpart to [Text]: besides the vendor prefixes it
// also removes any long opaque token, because an API that echoes a key back
// does not always echo one with a recognisable prefix. The message is
// re-joined on single spaces, which is acceptable for a sentence and would
// not be for a document.
func Message(s string) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		trimmed := strings.Trim(f, `"'.,:;()[]`)
		if trimmed != "" && LooksLikeKey(trimmed) {
			fields[i] = strings.ReplaceAll(f, trimmed, Placeholder)
			continue
		}
		for _, prefix := range keyPrefixes {
			if at := strings.Index(f, prefix); at >= 0 {
				end := at
				for end < len(f) && isKeyChar(f[end]) {
					end++
				}
				if end-at >= minKeyLen {
					fields[i] = f[:at] + Placeholder + f[end:]
					break
				}
			}
		}
	}
	return strings.Join(fields, " ")
}

// LooksLikeKey reports whether a token resembles an API key by the standard
// [Message] applies: anything carrying a known vendor prefix, or a long
// opaque run of key characters.
func LooksLikeKey(s string) bool {
	for _, prefix := range keyPrefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	if len(s) < minOpaqueLen {
		return false
	}
	return allKeyChars(s)
}

// NamesSecret reports whether an assignment's left-hand side declares a
// credential, as the names in a .env file do.
//
// The match is on whole words so that ordinary prose keeps its meaning, and it
// is generous about which words count: removing a value that turns out to be
// harmless costs the model a little context, while keeping one that turns out
// to be a key hands it to a third party permanently.
func NamesSecret(name string) bool {
	if name == "" || !isEnvName(name) {
		return false
	}
	upper := strings.ToUpper(name)
	for _, word := range []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL"} {
		if strings.HasSuffix(upper, word) {
			return true
		}
		for _, part := range strings.FieldsFunc(upper, func(r rune) bool { return r == '_' || r == '.' || r == '-' }) {
			if part == word {
				return true
			}
		}
	}
	return false
}

// redactToken rewrites one whitespace-delimited token, reporting whether the
// secret it names is still to come in the next token.
func redactToken(token string) (text string, valueFollows bool) {
	if eq := strings.IndexByte(token, '='); eq >= 0 {
		name, value := token[:eq], token[eq+1:]
		if !NamesSecret(name) {
			return token, false
		}
		if value == "" {
			// KEY= — the value is quoted, so it is the next token.
			return token, true
		}
		return name + "=" + Placeholder, false
	}

	lead, core, trail := trimPunct(token)
	if looksLikeSecret(core) {
		return lead + Placeholder + trail, false
	}
	return token, false
}

// looksLikeSecret reports whether a bare token is recognisably an API key by
// the conservative standard [Text] applies: a vendor prefix and a real key's
// length, nothing else.
func looksLikeSecret(s string) bool {
	if len(s) < minKeyLen {
		return false
	}
	prefixed := false
	for _, prefix := range keyPrefixes {
		if strings.HasPrefix(s, prefix) {
			prefixed = true
			break
		}
	}
	return prefixed && allKeyChars(s)
}

// isEnvName reports whether name is shaped like an environment variable, which
// keeps the assignment rule away from expressions and comparisons in code.
func isEnvName(name string) bool {
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// allKeyChars reports whether every byte of s could appear in an API key.
func allKeyChars(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isKeyChar(s[i]) {
			return false
		}
	}
	return true
}

func isKeyChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_'
}

// trimPunct splits the punctuation a token picked up from the sentence around
// it away from the token itself, so that a redaction can replace the value
// without eating the comma after it.
func trimPunct(token string) (lead, core, trail string) {
	const (
		leading  = "([{<"
		trailing = ".,;:!?)]}>"
	)
	core = strings.TrimLeft(token, leading)
	lead = token[:len(token)-len(core)]
	trimmed := strings.TrimRight(core, trailing)
	trail, core = core[len(trimmed):], trimmed
	return lead, core, trail
}

// bearerAt reports the length of a bearer marker starting at i, or zero if
// there is none. The comparison ignores case because the header is written
// every way by hand.
func bearerAt(s string, i int) int {
	if len(s)-i < len(bearerMarker) {
		return 0
	}
	if !strings.EqualFold(s[i:i+len(bearerMarker)], bearerMarker) {
		return 0
	}
	return len(bearerMarker)
}

// endOfToken returns the index just past the token starting at i.
func endOfToken(s string, i int) int {
	for i < len(s) && !isTokenEnd(s[i]) {
		i++
	}
	return i
}

// isTokenEnd reports whether b closes a credential token. Quotes count as
// delimiters, not token characters: credentials never contain them, and
// consuming a closing quote would corrupt the surrounding text it belongs to.
func isTokenEnd(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\'', '"', '`':
		return true
	}
	return false
}

// isQuote reports whether b is one of the quotes isTokenEnd treats as a
// delimiter.
func isQuote(b byte) bool {
	switch b {
	case '\'', '"', '`':
		return true
	}
	return false
}
