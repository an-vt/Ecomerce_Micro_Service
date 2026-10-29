// Package config loads typed settings from the environment (docs/11-infrastructure.md §3).
//
// Every setting accepts a <NAME>_FILE variant: when set, the value is read from that file (Docker / Kubernetes
// secrets). Loaders collect all problems and report them together via Err, so a misconfigured container fails
// once with the full list instead of one variable at a time.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Loader reads settings. Create it with NewLoader (process environment) or NewLoaderFrom (tests).
type Loader struct {
	lookup   func(string) (string, bool)
	readFile func(string) ([]byte, error)
	problems []string
}

// NewLoader reads from the process environment and the file system.
func NewLoader() *Loader { return NewLoaderFrom(os.LookupEnv, os.ReadFile) }

// NewLoaderFrom reads from custom sources.
func NewLoaderFrom(lookup func(string) (string, bool), readFile func(string) ([]byte, error)) *Loader {
	return &Loader{lookup: lookup, readFile: readFile}
}

// Map returns a lookup over m, for tests.
func Map(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func (l *Loader) fail(format string, args ...any) {
	l.problems = append(l.problems, fmt.Sprintf(format, args...))
}

// raw returns the value of key (or of key_FILE) and whether it was set and non-empty.
func (l *Loader) raw(key string) (string, bool) {
	if path, ok := l.lookup(key + "_FILE"); ok && path != "" {
		b, err := l.readFile(path)
		if err != nil {
			l.fail("%s_FILE: %v", key, err)
			return "", false
		}
		return strings.TrimSpace(string(b)), true
	}
	if v, ok := l.lookup(key); ok && v != "" {
		return v, true
	}
	return "", false
}

// String returns the value or def.
func (l *Loader) String(key, def string) string {
	if v, ok := l.raw(key); ok {
		return v
	}
	return def
}

// Required returns the value and records a problem when it is missing.
func (l *Loader) Required(key string) string {
	v, ok := l.raw(key)
	if !ok {
		l.fail("%s is required", key)
	}
	return v
}

// Int returns the value as int or def.
func (l *Loader) Int(key string, def int) int {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.fail("%s: %q is not an integer", key, v)
		return def
	}
	return n
}

// Float returns the value as float64 or def.
func (l *Loader) Float(key string, def float64) float64 {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		l.fail("%s: %q is not a number", key, v)
		return def
	}
	return f
}

// Bool returns the value as bool or def.
func (l *Loader) Bool(key string, def bool) bool {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail("%s: %q is not a boolean", key, v)
		return def
	}
	return b
}

// DurationMS reads an integer number of milliseconds (the *_MS variables).
func (l *Loader) DurationMS(key string, def time.Duration) time.Duration {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		l.fail("%s: %q is not a non-negative number of milliseconds", key, v)
		return def
	}
	return time.Duration(n) * time.Millisecond
}

// Strings splits a comma-separated value, trimming blanks.
func (l *Loader) Strings(key string, def []string) []string {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Err returns all collected problems as one error, or nil.
func (l *Loader) Err() error {
	if len(l.problems) == 0 {
		return nil
	}
	return errors.New("invalid configuration: " + strings.Join(l.problems, "; "))
}
