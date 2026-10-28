// Package presets embeds complete original method bundles. Inclusion is not a
// claim that any paid provider/model has passed professional quality evaluation.
package presets

import (
	"embed"
	"io/fs"
	"novelstudio/internal/skills"
	"strings"
)

//go:embed shared
var files embed.FS

func Names() ([]string, error) {
	entries, err := files.ReadDir("shared")
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}
func Bundle(name string) (skills.Bundle, error) {
	bundle := skills.Bundle{Files: []skills.File{}}
	if !skills.SafePath(name) || strings.Contains(name, "/") {
		return bundle, skills.ErrPackage
	}
	root := "shared/" + name + "/"
	err := fs.WalkDir(files, strings.TrimSuffix(root, "/"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		bundle.Files = append(bundle.Files, skills.File{Path: strings.TrimPrefix(path, root), Data: data})
		return nil
	})
	if err != nil {
		return bundle, err
	}
	return bundle, bundle.Validate()
}
