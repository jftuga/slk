package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// saveGlobalWidth rewrites the [sidebar] width line in config.toml.
// If the file has no width line under [sidebar], it appends one.
// Existing comments and ordering are preserved (textual rewrite, not
// TOML re-marshal).
func saveGlobalWidth(configPath string, width int) error {
	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
			return err
		}
		data = nil
	} else if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")

	inSidebar := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inSidebar = trimmed == "[sidebar]"
			continue
		}
		if !inSidebar {
			continue
		}
		if strings.HasPrefix(trimmed, "width") && strings.Contains(trimmed, "=") {
			lines[i] = "width = " + strconv.Itoa(width)
			return os.WriteFile(configPath, []byte(strings.Join(lines, "\n")), 0644)
		}
	}
	// No [sidebar] width line found — append or insert.
	// If [sidebar] exists, insert width after the header.
	for i, line := range lines {
		if strings.TrimSpace(line) == "[sidebar]" {
			newLines := make([]string, 0, len(lines)+1)
			newLines = append(newLines, lines[:i+1]...)
			newLines = append(newLines, "width = "+strconv.Itoa(width))
			newLines = append(newLines, lines[i+1:]...)
			return os.WriteFile(configPath, []byte(strings.Join(newLines, "\n")), 0644)
		}
	}
	// No [sidebar] section at all — append a new one.
	lines = append(lines, "", "[sidebar]", "width = "+strconv.Itoa(width))
	return os.WriteFile(configPath, []byte(strings.Join(lines, "\n")), 0644)
}

// saveWorkspaceWidth rewrites or appends a sidebar_width entry in
// [workspaces.<tomlKey>]. Mirrors saveWorkspaceTheme.
func saveWorkspaceWidth(configPath, tomlKey, teamID, teamName string, width int) error {
	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
			return err
		}
		data = nil
	} else if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")

	header := fmt.Sprintf("[workspaces.%s]", tomlKey)

	sectionStart := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			sectionStart = i
			break
		}
	}

	if sectionStart >= 0 {
		end := len(lines)
		for j := sectionStart + 1; j < len(lines); j++ {
			t := strings.TrimSpace(lines[j])
			if t == "" || strings.HasPrefix(t, "[") {
				end = j
				break
			}
		}
		updated := false
		for j := sectionStart + 1; j < end; j++ {
			t := strings.TrimSpace(lines[j])
			if strings.HasPrefix(t, "sidebar_width") && strings.Contains(t, "=") {
				lines[j] = "sidebar_width = " + strconv.Itoa(width)
				updated = true
				break
			}
		}
		if !updated {
			newLines := make([]string, 0, len(lines)+1)
			newLines = append(newLines, lines[:sectionStart+1]...)
			newLines = append(newLines, "sidebar_width = "+strconv.Itoa(width))
			newLines = append(newLines, lines[sectionStart+1:]...)
			lines = newLines
		}
		return os.WriteFile(configPath, []byte(strings.Join(lines, "\n")), 0644)
	}

	// No existing section — append a legacy-keyed block.
	if len(lines) > 0 && lines[len(lines)-1] != "" {
		lines = append(lines, "")
	}
	safeName := sanitizeComment(teamName)
	if safeName == "" {
		safeName = teamID
	}
	commentLine := "# " + safeName
	legacyHeader := fmt.Sprintf("[workspaces.%s]", teamID)
	lines = append(lines, commentLine, legacyHeader, "sidebar_width = "+strconv.Itoa(width))
	return os.WriteFile(configPath, []byte(strings.Join(lines, "\n")), 0644)
}
