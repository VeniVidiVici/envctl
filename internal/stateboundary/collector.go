package stateboundary

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type Spec struct {
	ID          string
	Path        string
	AllowAbsent bool
}

type Issue struct {
	ID      string
	Message string
}

type Collector struct {
	home     string
	specs    []Spec
	lstat    func(string) (fs.FileInfo, error)
	readlink func(string) (string, error)
}

func DefaultCollector() (Collector, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Collector{}, fmt.Errorf("find home directory: %w", err)
	}
	return NewCollector(home, []Spec{{
		ID:          "opencode-data",
		Path:        filepath.Join(home, ".local", "share", "opencode"),
		AllowAbsent: true,
	}}), nil
}

func NewCollector(home string, specs []Spec) Collector {
	return Collector{
		home:     filepath.Clean(home),
		specs:    append([]Spec(nil), specs...),
		lstat:    os.Lstat,
		readlink: os.Readlink,
	}
}

func (c Collector) Collect() []Issue {
	var issues []Issue
	for _, spec := range c.specs {
		if issue := c.inspect(spec); issue != nil {
			issues = append(issues, *issue)
		}
	}
	return issues
}

func (c Collector) inspect(spec Spec) *Issue {
	path := filepath.Clean(spec.Path)
	relative, err := filepath.Rel(c.home, path)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return &Issue{
			ID:      spec.ID,
			Message: fmt.Sprintf("configured path escapes the home directory: %s", path),
		}
	}

	current := c.home
	components := strings.Split(relative, string(filepath.Separator))
	for index, component := range components {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := c.lstat(current)
		if err != nil {
			if os.IsNotExist(err) && spec.AllowAbsent {
				return nil
			}
			return &Issue{
				ID:      spec.ID,
				Message: fmt.Sprintf("cannot inspect machine-local path %s: %v", current, err),
			}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := c.readlink(current)
			if readErr != nil {
				target = "<unreadable>"
			}
			return &Issue{
				ID: spec.ID,
				Message: fmt.Sprintf(
					"machine-local path component %s is a symbolic link to %s",
					current, target,
				),
			}
		}
		if !info.IsDir() {
			label := "path component"
			if index == len(components)-1 {
				label = "path"
			}
			return &Issue{
				ID: spec.ID,
				Message: fmt.Sprintf(
					"machine-local %s %s is not a directory", label, current,
				),
			}
		}
	}
	return nil
}
