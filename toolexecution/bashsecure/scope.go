package bashsecure

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Scope defines the filesystem roots available to a secured Bash command.
type Scope struct {
	ProjectRoot    string
	TemporaryRoot  string
	TemporaryRoots []string
}

// NewScope canonicalizes the project and temporary roots before they are used
// for path and symlink comparisons.
func NewScope(projectRoot, temporaryRoot string, additionalTemporaryRoots ...string) (Scope, error) {
	projectRoot, err := canonicalDirectory(projectRoot)
	if err != nil {
		return Scope{}, fmt.Errorf("project root: %w", err)
	}
	temporaryRoot, err = canonicalDirectory(temporaryRoot)
	if err != nil {
		return Scope{}, fmt.Errorf("temporary root: %w", err)
	}

	temporaryRoots := []string{temporaryRoot}
	for _, root := range additionalTemporaryRoots {
		root, err = canonicalDirectory(root)
		if err != nil {
			return Scope{}, fmt.Errorf("additional temporary root: %w", err)
		}
		alreadyAdded := false
		for _, existing := range temporaryRoots {
			if root == existing {
				alreadyAdded = true
				break
			}
		}
		if !alreadyAdded {
			temporaryRoots = append(temporaryRoots, root)
		}
	}
	return Scope{ProjectRoot: projectRoot, TemporaryRoot: temporaryRoot, TemporaryRoots: temporaryRoots}, nil
}

// ResolvePath resolves path and rejects paths or symlinks outside the scope.
func (scope Scope) ResolvePath(path string, allowMissing bool) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(scope.ProjectRoot, path)
	}
	path = filepath.Clean(path)

	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		if !scope.Allows(resolved) {
			return "", fmt.Errorf("path %q resolves outside the project and temporary folders", path)
		}
		return resolved, nil
	}
	if !allowMissing || !os.IsNotExist(err) {
		return "", err
	}

	ancestor := path
	var suffix []string
	for {
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", err
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = parent
		resolvedAncestor, ancestorErr := filepath.EvalSymlinks(ancestor)
		if ancestorErr != nil {
			if os.IsNotExist(ancestorErr) {
				continue
			}
			return "", ancestorErr
		}
		resolved = resolvedAncestor
		for index := len(suffix) - 1; index >= 0; index-- {
			resolved = filepath.Join(resolved, suffix[index])
		}
		if !scope.Allows(resolved) {
			return "", fmt.Errorf("path %q resolves outside the project and temporary folders", path)
		}
		return resolved, nil
	}
}

// Allows reports whether path is within the project or a temporary root.
func (scope Scope) Allows(path string) bool {
	if PathWithin(path, scope.ProjectRoot) {
		return true
	}
	for _, root := range scope.TemporaryRoots {
		if PathWithin(path, root) {
			return true
		}
	}
	return false
}

// PathWithin reports whether path is root itself or one of its descendants.
func PathWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func canonicalDirectory(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	return filepath.Clean(resolved), nil
}
