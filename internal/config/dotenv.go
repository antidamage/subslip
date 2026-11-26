// Package config loads SubSlip's settings from the environment and an
// optional .env file.
//
// File map:
//   - dotenv.go  (this file) reads KEY=VALUE lines from a .env file.
//   - config.go  turns the merged values into a typed Config with defaults.
//
// Used by cmd/subslip (serve, doctor, cache) and by nothing else: every other
// package receives the values it needs as plain arguments.
package config

import (
	"bufio"
	"os"
	"strings"
)

// ReadDotenv parses a .env file into a map. Blank lines and lines starting
// with # are skipped, an optional leading "export " is dropped, and a value
// wrapped in matching single or double quotes is unwrapped. A missing file is
// not an error: it returns an empty map and ok=false.
func ReadDotenv(path string) (values map[string]string, ok bool, err error) {
	values = map[string]string{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return values, false, nil
	}
	if err != nil {
		return values, false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, found := parseDotenvLine(scanner.Text())
		if found {
			values[key] = value
		}
	}
	return values, true, scanner.Err()
}

// parseDotenvLine splits one line into key and value. It reports found=false
// for comments, blank lines and lines with no '='.
func parseDotenvLine(line string) (key, value string, found bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	key, value, found = strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' || first == '\'') && first == last {
			return key, value[1 : len(value)-1], key != ""
		}
	}
	// An unquoted value may carry a trailing " # comment".
	if i := strings.Index(value, " #"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	return key, value, key != ""
}
