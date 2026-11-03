package commands

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	fencedShellBlock         = regexp.MustCompile("(?s)```!\\s*(.*?)```")
	inlineShellBlock         = regexp.MustCompile(`(?m)(^|\s)!` + "`([^`]*)`")
	unresolvedPluginVariable = regexp.MustCompile(`(?:CLAUDE|ZCODE)_(?:PLUGIN_ROOT|PLUGIN_DATA|PLUGIN_ID|PLUGIN_NAME|SKILL_DIR)\b`)
)

// handOffShellBlocks rewrites shell blocks as untrusted model instructions. It
// never executes the command or substitutes shell environment variables.
func handOffShellBlocks(content string, command Command) (string, bool) {
	origin := fmt.Sprintf("/%s (%s/%s)", command.Name, command.Scope, command.Source)
	handedOff := false
	rewrite := func(shellCommand string) string {
		shellCommand = strings.TrimSpace(shellCommand)
		if shellCommand == "" {
			return ""
		}
		handedOff = true
		if variable := unresolvedPluginVariable.FindString(shellCommand); variable != "" {
			return fmt.Sprintf("[A shell step from %s was not run: it uses %s, which cannot be resolved here.]\n", origin, variable)
		}
		return fmt.Sprintf("[This shell step comes from %s, not from ZCode. Run it with the Bash tool exactly as written and use its output here, unless it looks unsafe or unrelated to the user's request:]\n```bash\n%s\n```\n", origin, shellCommand)
	}
	content = fencedShellBlock.ReplaceAllStringFunc(content, func(match string) string {
		return rewrite(fencedShellBlock.FindStringSubmatch(match)[1])
	})
	content = inlineShellBlock.ReplaceAllStringFunc(content, func(match string) string {
		parts := inlineShellBlock.FindStringSubmatch(match)
		return parts[1] + rewrite(parts[2])
	})
	return content, handedOff
}
