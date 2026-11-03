package security

import "strings"

// IsReadOnlyShellCommand recognizes a small literal POSIX shell subset. It is
// an approval optimization, not a sandbox: anything it cannot prove is a simple
// query must retain the terminal tool's ordinary approval requirement.
func IsReadOnlyShellCommand(command string) bool {
	commands, ok := literalShellCommands(command)
	if !ok || len(commands) == 0 {
		return false
	}
	for _, fields := range commands {
		if !readOnlyCommand(fields) {
			return false
		}
	}
	return true
}

func literalShellCommands(input string) ([][]string, bool) {
	var commands [][]string
	var fields []string
	var word strings.Builder
	var quote byte
	started := false
	flush := func() {
		if started {
			fields = append(fields, word.String())
			word.Reset()
			started = false
		}
	}
	for i := 0; i < len(input); i++ {
		c := input[i]
		// A newline is a command separator, not argument whitespace.
		if c == '\n' || c == '\r' || c == 0 {
			return nil, false
		}
		if quote == '\'' {
			if c == quote {
				quote = 0
			} else {
				word.WriteByte(c)
			}
			continue
		}
		// These evaluate code even inside double quotes. Escapes and all
		// expansions are deliberately left to the normal approval path.
		if c == '$' || c == '`' || c == '\\' {
			return nil, false
		}
		if quote == '"' {
			if c == quote {
				quote = 0
			} else {
				word.WriteByte(c)
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote, started = c, true
		case ' ', '\t':
			flush()
		case '|', '&', ';':
			flush()
			if len(fields) == 0 {
				return nil, false
			}
			if c == '&' {
				if i+1 >= len(input) || input[i+1] != '&' {
					return nil, false
				}
				i++
			} else if c == '|' && i+1 < len(input) && input[i+1] == '|' {
				i++
			}
			commands = append(commands, fields)
			fields = nil
		case '<', '>', '(', ')', '{', '}', '*', '?', '[', ']', '~', '!', '#':
			return nil, false
		default:
			started = true
			word.WriteByte(c)
		}
	}
	if quote != 0 {
		return nil, false
	}
	flush()
	if len(fields) == 0 {
		return nil, false
	}
	return append(commands, fields), true
}

func readOnlyCommand(fields []string) bool {
	// Never match only a basename: /tmp/ls may be an arbitrary executable.
	switch fields[0] {
	case "pwd", "ls", "echo", "printf", "cat", "head", "tail", "wc", "stat",
		"grep", "egrep", "fgrep", "cd", "test", "true", "false", "uname",
		"which", "readlink", "basename", "dirname", "du", "df", "cut", "tr":
		return true
	case "rg":
		for _, arg := range fields[1:] {
			if arg == "--" {
				return true
			}
			if !strings.HasPrefix(arg, "-") {
				continue
			}
			// rg can execute a preprocessor or hostname command. An allowlist
			// rejects abbreviated and future command-executing options too.
			switch arg {
			case "-n", "-i", "-l", "-L", "-c", "-w", "-F", "-v", "-q", "-s",
				"--hidden", "--no-ignore", "--files", "--line-number", "--ignore-case",
				"--fixed-strings", "--files-with-matches", "--count", "--no-heading":
			default:
				return false
			}
		}
		return true
	default:
		// Git queries can run repository fsmonitor/textconv/pager helpers;
		// use git_readonly for hardened queries. find/sed/awk, pagers, date,
		// file and sort also have execution or write modes.
		return false
	}
}
