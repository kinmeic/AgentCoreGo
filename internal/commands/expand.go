package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
)

var positionalArgument = regexp.MustCompile(`\$(\d+)`)

// Expand loads and safely expands a custom markdown command.
func (l *Loader) Expand(name, args string) (Expansion, error) {
	command, raw, err := l.Load(name)
	if err != nil {
		return Expansion{}, err
	}
	return Expand(command, raw, args)
}

// Expand rewrites shell blocks as untrusted hand-off notes before substituting arguments.
func Expand(command Command, raw, args string) (Expansion, error) {
	raw, _ = handOffShellBlocks(raw, command)
	args = strings.TrimSpace(args)
	positional := SplitArguments(args)
	used := strings.Contains(raw, "$ARGUMENTS")
	body := strings.ReplaceAll(raw, "$ARGUMENTS", args)
	body = positionalArgument.ReplaceAllStringFunc(body, func(match string) string {
		used = true
		index := int(match[1]-'0') - 1
		if index >= 0 && index < len(positional) {
			return positional[index]
		}
		return ""
	})
	if args != "" && !used {
		body = strings.TrimSpace(body) + "\n\nUser arguments:\n" + args
	}
	expanded := "Run custom command /" + command.Name + ".\n"
	if len(command.Skills) > 0 {
		expanded += "Required skills: `" + strings.Join(command.Skills, "`, `") + "`. Load them before following the command body.\n"
	}
	expanded += "\n" + strings.TrimSpace(body)
	rawDigest := sha256.Sum256([]byte(raw))
	digest := sha256.Sum256([]byte(expanded))
	return Expansion{Command: command, Raw: raw, Expanded: expanded,
		RawDigest: hex.EncodeToString(rawDigest[:]), Digest: hex.EncodeToString(digest[:]),
		Arguments: positional, UsedArgumentsPlaceholder: used}, nil
}

// SplitArguments handles quotes and backslash escaping without invoking a shell.
func SplitArguments(input string) []string {
	var result []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			result = append(result, current.String())
			current.Reset()
		}
	}
	for _, r := range input {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		current.WriteRune(r)
	}
	if escaped {
		current.WriteRune('\\')
	}
	flush()
	return result
}
