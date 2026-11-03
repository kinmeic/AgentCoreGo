package commands

import "sort"

var builtinCommands = []Command{
	{Name: "help", Usage: "/help [command]", Description: "Show command help", Builtin: true},
	{Name: "login", Usage: "/login [...]", Description: "Set up a Coding Plan provider", Builtin: true},
	{Name: "logout", Usage: "/logout", Description: "Remove shared login credentials", Builtin: true},
	{Name: "compact", Usage: "/compact [instructions]", Description: "Compact the current conversation", Builtin: true},
	{Name: "init", Usage: "/init [notes]", Description: "Create or update workspace AGENTS.md", Builtin: true},
	{Name: "expert", Usage: "/expert [status|resume|stop|<task>]", Description: "Run or manage expert workflow", Builtin: true},
	{Name: "effort", Aliases: []string{"variant"}, Usage: "/effort [list|<level>]", Description: "Show or switch reasoning effort", Builtin: true},
	{Name: "dwf", Usage: "/dwf [list|cancel [runId]|resume <runId>]", Description: "Manage dynamic workflows", Builtin: true},
	{Name: "fork", Usage: "/fork [latest|checkpointId]", Description: "Fork from a workspace snapshot", Builtin: true},
	{Name: "locale", Aliases: []string{"language"}, Usage: "/locale [auto|en-US|zh-CN]", Description: "Show or switch UI locale", Builtin: true},
	{Name: "mcp", Usage: "/mcp [list|status|connect|disconnect]", Description: "Manage MCP servers", Builtin: true},
	{Name: "plugins", Aliases: []string{"plugin"}, Usage: "/plugins [list|enable|disable]", Description: "Manage plugins", Builtin: true},
	{Name: "mode", Usage: "/mode [plan|build|edit|yolo|guarded]", Description: "Show or switch permission mode", Builtin: true},
	{Name: "model", Usage: "/model [list|provider/model]", Description: "Show or switch model", Builtin: true},
	{Name: "new", Aliases: []string{"clear"}, Usage: "/new", Description: "Start a fresh session", Builtin: true},
	{Name: "resume", Aliases: []string{"continue"}, Usage: "/resume [sessionId]", Description: "Resume a saved session", Builtin: true},
	{Name: "rewind", Usage: "/rewind [latest|checkpointId]", Description: "Inspect or restore snapshots", Builtin: true},
	{Name: "skill", Usage: "/skill [<skill-name> [task]]", Description: "List or load a skill", Builtin: true},
	{Name: "goal", Aliases: []string{"target"}, Usage: "/goal [pause|resume|clear|replace|<objective>]", Description: "Manage session goal", Builtin: true},
	{Name: "workflow", Usage: "/workflow <task>", Description: "Design and launch a dynamic workflow", Builtin: true},
}

// Builtins returns the reference-compatible command surface plus AgentCore management commands.
func Builtins() []Command {
	result := append([]Command(nil), builtinCommands...)
	for _, command := range []Command{
		{Name: "trust", Usage: "/trust [status|review|grant|revoke]", Description: "Manage workspace trust", Builtin: true},
		{Name: "snapshot", Usage: "/snapshot [create|list|restore <id>]", Description: "Manage workspace snapshots", Builtin: true},
		{Name: "agents", Usage: "/agents [list|stop <run>]", Description: "Manage subagents", Builtin: true},
		{Name: "trace", Usage: "/trace [show <run>|export <run>]", Description: "Inspect model and tool traces", Builtin: true},
		{Name: "benchmark", Usage: "/benchmark [plan|run|show|cancel]", Description: "Run and inspect benchmarks", Builtin: true},
	} {
		result = append(result, command)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func Builtin(name string) (Command, bool) {
	for _, command := range Builtins() {
		if command.Name == name {
			return command, true
		}
		for _, alias := range command.Aliases {
			if alias == name {
				command.Name = name
				return command, true
			}
		}
	}
	return Command{}, false
}
