package workspace

import (
	"fmt"
	"path"
	"strings"
)

type ignorePattern struct {
	pattern string
	rooted  bool
	dirOnly bool
}

type ignoreMatcher struct {
	patterns []ignorePattern
}

func newIgnoreMatcher(patterns []string) (ignoreMatcher, error) {
	matcher := ignoreMatcher{patterns: make([]ignorePattern, 0, len(patterns))}
	for _, original := range patterns {
		if original == "" || strings.ContainsRune(original, '\x00') {
			return ignoreMatcher{}, fmt.Errorf("empty or NUL-containing pattern %q", original)
		}
		pattern := ignorePattern{pattern: original}
		pattern.rooted = strings.HasPrefix(pattern.pattern, "/")
		pattern.pattern = strings.TrimPrefix(pattern.pattern, "/")
		pattern.dirOnly = strings.HasSuffix(pattern.pattern, "/")
		pattern.pattern = strings.TrimSuffix(pattern.pattern, "/")
		if pattern.pattern == "" {
			return ignoreMatcher{}, fmt.Errorf("pattern %q has no match expression", original)
		}
		if _, err := path.Match(pattern.pattern, "validation"); err != nil {
			return ignoreMatcher{}, err
		}
		matcher.patterns = append(matcher.patterns, pattern)
	}
	return matcher, nil
}

func (m ignoreMatcher) Match(relative string, directory bool) bool {
	for _, pattern := range m.patterns {
		if pattern.dirOnly && !directory {
			continue
		}
		if pattern.rooted || strings.Contains(pattern.pattern, "/") {
			matched, _ := path.Match(pattern.pattern, relative)
			if matched {
				return true
			}
			continue
		}
		for _, component := range strings.Split(relative, "/") {
			matched, _ := path.Match(pattern.pattern, component)
			if matched {
				return true
			}
		}
	}
	return false
}

func isGitMetadata(relative string) bool {
	for _, component := range strings.Split(relative, "/") {
		if component == ".git" {
			return true
		}
	}
	return false
}
