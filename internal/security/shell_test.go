package security

import "testing"

func TestReadOnlyShellCommandClassifier(t *testing.T) {
	for _, command := range []string{
		"pwd", "ls -la", "cat file.txt", "grep -R 'needle' .", "rg TODO internal", "rg -n TODO internal",
		"echo '$(literal)'", `echo "literal text"`, "cd internal && rg foo", "cat a | head -n 10", "echo ''",
	} {
		if !IsReadOnlyShellCommand(command) {
			t.Errorf("expected read-only command: %s", command)
		}
	}
	for _, command := range []string{
		"rm -rf /tmp/x", "echo $(rm -rf /tmp/x)", "echo data > file", "cat /etc/passwd | rm x", "git reset --hard",
		"find . -delete", "sed -i 's/a/b/' file", "python -c 'print(1)'", "echo `id`", "sleep 2 &", "sort -o out file",
		`echo "$(touch marker)"`, "echo \"`touch marker`\"", "echo ok\ntouch marker",
		"PATH=/tmp ls", "/tmp/ls", "git branch injected", "git remote add injected /tmp/repo", "git tag injected",
		"git diff --output=marker", "git log --ext-diff", "git status --short",
		"find . -fprint marker", `sed -n 'w marker' file`, `awk 'BEGIN { print "x" > "marker" }'`,
		"rg --pre=touch needle", "sort -omarker file", "sort --compress-program=touch file",
		"date -s 2026-01-01", "file -C", "less file", "man ls", "echo ok # ignored ; touch marker",
	} {
		if IsReadOnlyShellCommand(command) {
			t.Errorf("expected side-effecting command: %s", command)
		}
	}
}
