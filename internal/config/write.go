package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/viper"
)

// FilePath returns the path of the config file that Load reads. When no
// config file exists, it returns the path of the TOML file that Load would
// read: $XDG_CONFIG_HOME/chroncal/config.toml.
func FilePath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", fmt.Errorf("find the config directory: %w", err)
	}
	dir = filepath.Join(dir, "chroncal")
	v := newViper()
	v.AddConfigPath(dir)
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if errors.As(err, &notFound) {
			return filepath.Join(dir, "config.toml"), nil
		}
		// A malformed file still has a path. The writer reads the file
		// again and refuses it with its own error.
		if used := v.ConfigFileUsed(); used != "" {
			return used, nil
		}
		return "", fmt.Errorf("read config file: %w", err)
	}
	return v.ConfigFileUsed(), nil
}

// EnableAllowPlaintext writes security.allow_plaintext = true to the config
// file and returns the path of that file. It creates the file with mode 0600
// when no file exists.
//
// The function edits only that one key. It keeps the comments, the order,
// and the other keys of the file. It parses the result before it writes, and
// it refuses the write when any other value changes or when the result does
// not parse. The user then edits the file by hand.
func EnableAllowPlaintext() (string, error) {
	path, err := FilePath()
	if err != nil {
		return "", err
	}
	if ext := filepath.Ext(path); ext != ".toml" && ext != "" {
		return path, fmt.Errorf(
			"set security.allow_plaintext by hand: chroncal edits only a TOML config file, not %s", path)
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}

	mode := fs.FileMode(0o600)
	original, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		original = nil
	case err != nil:
		return path, fmt.Errorf("read config file: %w", err)
	default:
		info, statErr := os.Stat(path)
		if statErr != nil {
			return path, fmt.Errorf("read config file: %w", statErr)
		}
		mode = info.Mode().Perm()
	}

	updated, err := setSecurityAllowPlaintext(original)
	if err != nil {
		return path, fmt.Errorf("set security.allow_plaintext in %s: %w", path, err)
	}
	if bytes.Equal(updated, original) {
		return path, nil
	}
	if err := writeFileAtomic(path, updated, mode); err != nil {
		return path, fmt.Errorf("write config file: %w", err)
	}
	return path, nil
}

var (
	tomlTableHeader = regexp.MustCompile(`^\s*\[\s*([^\[\]]+?)\s*\]\s*(#.*)?$`)
	tomlArrayHeader = regexp.MustCompile(`^\s*\[\[`)
	// allowPlaintextLine matches the key line in the [security] table and
	// the dotted key line at the top level. Group 1 is the text before the
	// value, group 2 is the value, and group 3 is the text after it.
	allowPlaintextLine = regexp.MustCompile(
		`^(\s*(?:security\s*\.\s*)?allow_plaintext\s*=\s*)(true|false)(\s*(?:#.*)?)$`)
)

// setSecurityAllowPlaintext returns content with security.allow_plaintext set
// to true. It changes one line, inserts one line, or appends one table. It
// then parses both versions and returns an error when a value other than
// security.allow_plaintext differs.
func setSecurityAllowPlaintext(content []byte) ([]byte, error) {
	var before map[string]any
	if err := toml.Unmarshal(content, &before); err != nil {
		return nil, fmt.Errorf("the file does not parse: %w", err)
	}
	if before == nil {
		before = map[string]any{}
	}

	lines := strings.Split(string(content), "\n")
	table := ""
	headerLine := -1
	keyLine := -1
	for i, line := range lines {
		if tomlArrayHeader.MatchString(line) {
			table = "[["
			continue
		}
		if match := tomlTableHeader.FindStringSubmatch(line); match != nil {
			table = match[1]
			if table == "security" && headerLine < 0 {
				headerLine = i
			}
			continue
		}
		isDotted := strings.HasPrefix(strings.TrimSpace(line), "security")
		if (table == "security" && !isDotted) || (table == "" && isDotted) {
			if allowPlaintextLine.MatchString(line) && keyLine < 0 {
				keyLine = i
			}
		}
	}

	switch {
	case keyLine >= 0:
		lines[keyLine] = allowPlaintextLine.ReplaceAllString(lines[keyLine], "${1}true${3}")
	case headerLine >= 0:
		lines = append(lines[:headerLine+1],
			append([]string{"allow_plaintext = true"}, lines[headerLine+1:]...)...)
	default:
		text := strings.Join(lines, "\n")
		switch {
		case text == "":
		case strings.HasSuffix(text, "\n"):
			text += "\n"
		default:
			text += "\n\n"
		}
		lines = strings.Split(text+"[security]\nallow_plaintext = true\n", "\n")
	}
	updated := []byte(strings.Join(lines, "\n"))

	var after map[string]any
	if err := toml.Unmarshal(updated, &after); err != nil {
		return nil, fmt.Errorf("the edit does not parse: %w", err)
	}
	security, _ := before["security"].(map[string]any)
	if security == nil {
		if _, exists := before["security"]; exists {
			return nil, errors.New("security is not a table")
		}
		security = map[string]any{}
		before["security"] = security
	}
	security["allow_plaintext"] = true
	if !reflect.DeepEqual(before, after) {
		return nil, errors.New("the edit changes other values")
	}
	return updated, nil
}

// writeFileAtomic writes data to a temporary file in the directory of path,
// then renames it over path. A reader never sees a partial file.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
