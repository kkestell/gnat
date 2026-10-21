package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// envPath is gnat's own .env, which holds the credentials `gnat up` gives agents.
func envPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "gnat", ".env")
}

// setting is a variable from the environment or gnat's .env.
func setting(name string) (string, error) {
	if value := os.Getenv(name); value != "" {
		return value, nil
	}
	values, err := dotenv(envPath())
	return values[name], err
}

// dotenv reads a .env file: NAME=value lines, optionally after `export`, with # comments
// and single- or double-quoted values. A file that doesn't exist has no values.
func dotenv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(name)] = dotenvValue(strings.TrimSpace(value))
	}
	return values, nil
}

func dotenvValue(value string) string {
	if value == "" {
		return ""
	}
	if quote := value[0]; quote == '"' || quote == '\'' {
		if end := closingQuote(value, quote); end > 0 {
			inner := value[1:end]
			if quote == '\'' {
				return inner
			}
			return strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\n`, "\n", `\t`, "\t", `\r`, "\r").Replace(inner)
		}
	}
	// Unquoted, a # that follows whitespace starts a comment.
	if i := strings.Index(value, " #"); i >= 0 {
		value = value[:i]
	}
	if i := strings.Index(value, "\t#"); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}

// closingQuote is the index of the quote that ends the quoted value, or -1.
func closingQuote(value string, quote byte) int {
	for i := 1; i < len(value); i++ {
		switch {
		case value[i] == '\\' && quote == '"':
			i++
		case value[i] == quote:
			return i
		}
	}
	return -1
}
