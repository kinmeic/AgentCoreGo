package builtin

func outsideWorkspaceEnabled(options []bool) bool {
	return len(options) > 0 && options[0]
}
