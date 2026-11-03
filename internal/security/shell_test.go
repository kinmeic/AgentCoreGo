package security

import "testing"

func TestReadOnlyShellCommandClassifier(t *testing.T) {
	for _, command := range []string{
		"pwd", "ls -la", "cat file.txt", "grep -R 'needle' .", "rg TODO internal", "git status --short",
		"git diff HEAD -- file.go", "find . -name '*.go'", "sed -n '1,20p' main.go", "echo $HOME", "cd internal && rg foo",
	} {
		if !IsReadOnlyShellCommand(command) {
			t.Errorf("expected read-only command: %s", command)
		}
	}
	for _, command := range []string{
		"rm -rf /tmp/x", "echo $(rm -rf /tmp/x)", "echo data > file", "cat /etc/passwd | rm x", "git reset --hard",
		"find . -delete", "sed -i 's/a/b/' file", "python -c 'print(1)'", "echo `id`", "sleep 2 &", "sort -o out file",
	} {
		if IsReadOnlyShellCommand(command) {
			t.Errorf("expected side-effecting command: %s", command)
		}
	}
}
