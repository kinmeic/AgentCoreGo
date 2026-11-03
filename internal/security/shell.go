package security

import (
	"path/filepath"
	"strings"
	"unicode"
)

// IsReadOnlyShellCommand is a conservative read-only command classifier, not a
// complete shell parser. Anything uncertain is treated as side-effecting.
func IsReadOnlyShellCommand(command string) bool {
	segments, ok := splitShellSegments(command)
	if !ok || len(segments) == 0 {
		return false
	}
	for _, segment := range segments {
		fields, ok := shellFields(segment)
		if !ok || len(fields) == 0 {
			return false
		}
		for len(fields) > 0 && isEnvAssignment(fields[0]) {
			fields = fields[1:]
		}
		if len(fields) == 0 || !readOnlyCommand(fields) {
			return false
		}
	}
	return true
}

func splitShellSegments(input string) ([]string, bool) {
	var segments []string
	var current strings.Builder
	var quote rune
	escaped := false
	for _, r := range input {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			current.WriteRune(r)
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			current.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' || r == '`' {
			if r == '`' {
				return nil, false
			}
			quote = r
			current.WriteRune(r)
			continue
		}
		if r == '$' {
			// Command substitution and process substitution are rejected. Plain
			// environment expansion such as $HOME or ${HOME} is allowed.
			// A following '(' or '<' changes the evaluation semantics.
			current.WriteRune(r)
			continue
		}
		if r == '(' || r == '<' || r == '>' {
			return nil, false
		}
		if r == '|' || r == ';' || r == '&' {
			if current.Len() > 0 {
				segments = append(segments, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if escaped || quote != 0 {
		return nil, false
	}
	if current.Len() > 0 {
		segments = append(segments, current.String())
	}
	return segments, true
}

func shellFields(input string) ([]string, bool) {
	var fields []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			fields = append(fields, current.String())
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
			current.WriteRune(r)
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			current.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			current.WriteRune(r)
			continue
		}
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		current.WriteRune(r)
	}
	if escaped || quote != 0 {
		return nil, false
	}
	flush()
	return fields, true
}

func isEnvAssignment(value string) bool {
	parts := strings.SplitN(value, "=", 2)
	if len(parts) != 2 || parts[0] == "" {
		return false
	}
	for i, r := range parts[0] {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || (i > 0 && r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func readOnlyCommand(fields []string) bool {
	name := filepath.Base(fields[0])
	args := fields[1:]
	switch name {
	case "pwd", "ls", "dir", "echo", "printf", "cat", "head", "tail", "wc", "file", "stat",
		"grep", "egrep", "fgrep", "rg", "cd", "test", "[", "true", "false", "uname", "date",
		"which", "readlink", "basename", "dirname", "realpath", "du", "df", "uniq", "cut", "tr", "less", "more", "man":
		return true
	case "sort":
		for _, arg := range args {
			if arg == "-o" || strings.HasPrefix(arg, "--output") {
				return false
			}
		}
		return true
	case "find":
		for _, arg := range args {
			if arg == "-delete" || arg == "-exec" || arg == "-execdir" || arg == "-ok" || arg == "-okdir" {
				return false
			}
		}
		return true
	case "sed":
		hasPrintOnly := false
		for _, arg := range args {
			if arg == "-n" || strings.HasPrefix(arg, "-n") {
				hasPrintOnly = true
			}
			if arg == "-i" || strings.HasPrefix(arg, "--in-place") {
				return false
			}
		}
		return hasPrintOnly
	case "awk":
		return !strings.Contains(strings.Join(args, " "), "system(")
	case "command":
		return len(args) > 0 && args[0] == "-v"
	case "env", "printenv":
		return len(args) == 0
	case "git":
		if len(args) == 0 {
			return false
		}
		switch args[0] {
		case "status", "diff", "log", "branch", "show", "rev-parse", "ls-files", "describe", "remote", "tag":
			return true
		default:
			return false
		}
	default:
		return false
	}
}
