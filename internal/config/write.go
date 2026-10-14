package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/fsutil"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/viper"
)

// filePath returns the path of the config file that Load reads. When no
// config file exists, it returns the path of the TOML file that Load would
// read: $XDG_CONFIG_HOME/chroncal/config.toml.
func filePath() (string, error) {
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
//
// It also refuses the write when CHRONCAL_SECURITY_ALLOW_PLAINTEXT holds a
// false value. The variable overrides the file, so the write has no effect
// on the next launch. The user must unset the variable.
func EnableAllowPlaintext() (string, error) {
	if err := CheckAllowPlaintextEnv(); err != nil {
		return "", err
	}
	path, err := filePath()
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

	original, mode, err := readConfigFile(path)
	if err != nil {
		return path, fmt.Errorf("read config file: %w", err)
	}

	updated, err := setSecurityAllowPlaintext(original)
	if err != nil {
		return path, fmt.Errorf("set security.allow_plaintext in %s: %w", path, err)
	}
	if bytes.Equal(updated, original) {
		return path, nil
	}
	if err := fsutil.WriteFileAtomic(path, updated, mode, ".config-*.toml"); err != nil {
		return path, fmt.Errorf("write config file: %w", err)
	}
	return path, nil
}

// readConfigFile returns the content and the permission bits of the file at
// path. One open gives the two values. A file that is not there gives nil
// content and mode 0600, the mode of a new config file.
func readConfigFile(path string) ([]byte, fs.FileMode, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0o600, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	content, err := io.ReadAll(f)
	if err != nil {
		return nil, 0, err
	}
	return content, info.Mode().Perm(), nil
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

// allowPlaintextEnv is the variable that overrides security.allow_plaintext.
const allowPlaintextEnv = "CHRONCAL_SECURITY_ALLOW_PLAINTEXT"

// CheckAllowPlaintextEnv returns an error when allowPlaintextEnv holds a
// value other than a true bool. Load ignores an empty value. A false value
// overrides the config file, so the write has no effect. Load fails on a
// value that does not parse as a bool. Both errors tell the user to unset
// the variable.
func CheckAllowPlaintextEnv() error {
	value, ok := os.LookupEnv(allowPlaintextEnv)
	if !ok || value == "" {
		return nil
	}
	on, err := strconv.ParseBool(value)
	switch {
	case err != nil:
		return fmt.Errorf("%s=%q in the environment is not a bool, and chroncal cannot load the config while it is set; unset %s, then try again",
			allowPlaintextEnv, value, allowPlaintextEnv)
	case !on:
		return fmt.Errorf("%s=%q in the environment turns plaintext storage off and overrides the config file; unset %s, then try again",
			allowPlaintextEnv, value, allowPlaintextEnv)
	}
	return nil
}

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
	dottedLine := -1
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
		if table == "" && isDotted {
			dottedLine = i
		}
		if (table == "security" && !isDotted) || (table == "" && isDotted) {
			if allowPlaintextLine.MatchString(line) && keyLine < 0 {
				keyLine = i
			}
		}
	}

	var updated []byte
	switch {
	case keyLine >= 0:
		lines[keyLine] = allowPlaintextLine.ReplaceAllString(lines[keyLine], "${1}true${3}")
		updated = []byte(strings.Join(lines, "\n"))
	case headerLine >= 0:
		lines = slices.Insert(lines, headerLine+1, "allow_plaintext = true")
		updated = []byte(strings.Join(lines, "\n"))
	case dottedLine >= 0:
		// Dotted keys at the top level define the security table, so a
		// [security] header would define it again. Add one more dotted key.
		lines = slices.Insert(lines, dottedLine+1, "security.allow_plaintext = true")
		updated = []byte(strings.Join(lines, "\n"))
	default:
		// Append the table after one blank line.
		text := string(content)
		switch {
		case text == "":
		case strings.HasSuffix(text, "\n"):
			text += "\n"
		default:
			text += "\n\n"
		}
		updated = []byte(text + "[security]\nallow_plaintext = true\n")
	}

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
