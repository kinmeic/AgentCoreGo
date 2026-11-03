package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// GitFileStatus uses the index X/worktree Y states from porcelain v2.
type GitFileStatus struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	X       string `json:"x"`
	Y       string `json:"y"`
}

// GitStatus separates staged, unstaged, untracked and unmerged paths.
type GitStatus struct {
	Branch    string          `json:"branch,omitempty"`
	OID       string          `json:"oid,omitempty"`
	Clean     bool            `json:"clean"`
	Staged    []GitFileStatus `json:"staged"`
	Unstaged  []GitFileStatus `json:"unstaged"`
	Untracked []GitFileStatus `json:"untracked"`
	Conflicts []GitFileStatus `json:"conflicts"`
}

// GitCommit is one log entry.
type GitCommit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

func (s *Service) GitStatus(ctx context.Context) (GitStatus, error) {
	output, err := s.runGit(ctx, "status", "--porcelain=v2", "-z", "--branch", "--", ".")
	if err != nil {
		return GitStatus{}, err
	}
	var result GitStatus
	records := strings.Split(output, "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		switch {
		case strings.HasPrefix(record, "# branch.head "):
			result.Branch = strings.TrimPrefix(record, "# branch.head ")
		case strings.HasPrefix(record, "# branch.oid "):
			result.OID = strings.TrimPrefix(record, "# branch.oid ")
		case strings.HasPrefix(record, "1 ") || strings.HasPrefix(record, "2 "):
			fields := strings.SplitN(record, " ", 9)
			if len(fields) < 9 {
				continue
			}
			status := GitFileStatus{Path: fields[8], X: string(fields[1][0]), Y: string(fields[1][1])}
			if record[0] == '2' && i+1 < len(records) {
				i++
				status.OldPath = records[i]
			}
			result.add(status, false)
		case strings.HasPrefix(record, "u "):
			fields := strings.SplitN(record, " ", 10)
			if len(fields) < 10 {
				continue
			}
			status := GitFileStatus{Path: fields[9], X: string(fields[1][0]), Y: string(fields[1][1])}
			result.Conflicts = append(result.Conflicts, status)
			result.Unstaged = append(result.Unstaged, status)
		case strings.HasPrefix(record, "? "):
			result.Untracked = append(result.Untracked, GitFileStatus{Path: strings.TrimPrefix(record, "? ")})
		}
	}
	result.Clean = len(result.Staged)+len(result.Unstaged)+len(result.Untracked)+len(result.Conflicts) == 0
	return result, nil
}

func (g *GitStatus) add(status GitFileStatus, _ bool) {
	if g == nil {
		return
	}
	if status.X != "." && status.X != "?" {
		g.Staged = append(g.Staged, status)
	}
	if status.Y != "." && status.Y != "?" {
		g.Unstaged = append(g.Unstaged, status)
	}
}

// GitDiff returns worktree, staged, HEAD, or an explicit single-base diff.
func (s *Service) GitDiff(ctx context.Context, mode, base, path string) (string, error) {
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--relative"}
	switch mode {
	case "", "worktree":
	case "staged":
		args = append(args, "--cached")
	case "head":
		args = append(args, "HEAD")
	case "base":
		if !validGitRef(base) {
			return "", newError("workspace.git.diff", CodeInvalidArgument, base, "invalid base ref")
		}
		args = append(args, base)
	default:
		return "", newError("workspace.git.diff", CodeInvalidArgument, mode, "invalid diff mode")
	}
	if path != "" {
		resolved, _, err := s.resolve("workspace.git.diff", path, false)
		_ = resolved
		if err != nil {
			return "", err
		}
		args = append(args, "--", filepathToGitPath(path))
	}
	return s.runGit(ctx, args...)
}

// GitLog returns bounded commit history without allowing arbitrary refs or options.
func (s *Service) GitLog(ctx context.Context, before string, limit int, path string) ([]GitCommit, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	args := []string{"log", "--format=%H%x00%s%x00%an%x00%aI", "-n", strconv.Itoa(limit)}
	if before != "" {
		if !validGitRef(before) {
			return nil, newError("workspace.git.log", CodeInvalidArgument, before, "invalid before ref")
		}
		args = append(args, before)
	}
	if path != "" {
		if _, _, err := s.resolve("workspace.git.log", path, false); err != nil {
			return nil, err
		}
		args = append(args, "--", filepathToGitPath(path))
	}
	output, err := s.runGit(ctx, args...)
	if err != nil {
		return nil, err
	}
	var commits []GitCommit
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Split(line, "\x00")
		if len(fields) == 4 {
			commits = append(commits, GitCommit{Hash: fields[0], Subject: fields[1], Author: fields[2], Date: fields[3]})
		}
	}
	return commits, nil
}

func validGitRef(value string) bool {
	return value != "" && !strings.HasPrefix(value, "-") && !strings.Contains(value, "..") && !strings.ContainsAny(value, "\x00\r\n")
}

func filepathToGitPath(value string) string {
	return strings.ReplaceAll(filepath.Clean(value), "\\", "/")
}

func (s *Service) runGit(ctx context.Context, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, s.gitPath, args...)
	cmd.Dir = s.root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "LC_ALL=C"}
	stdout, stderr := &limitedBuffer{limit: s.maxGitOutputBytes}, &limitedBuffer{limit: 64 * 1024}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if stdout.truncated || stderr.truncated {
		return "", newError("workspace.git", CodeTooLarge, "", "git output exceeded limit")
	}
	if commandCtx.Err() != nil {
		return "", commandCtx.Err()
	}
	if err != nil {
		if strings.Contains(stderr.String(), "not a git repository") {
			return "", newError("workspace.git", CodeNotRepository, "", "not a Git repository")
		}
		return "", fmt.Errorf("git failed: %s", strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

type limitedBuffer struct {
	value     strings.Builder
	limit     int64
	truncated bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	remaining := b.limit - int64(b.value.Len())
	if remaining > 0 {
		if int64(len(data)) <= remaining {
			b.value.Write(data)
		} else {
			b.value.Write(data[:remaining])
			b.truncated = true
		}
	} else {
		b.truncated = true
	}
	return len(data), nil
}
func (b *limitedBuffer) String() string { return b.value.String() }
