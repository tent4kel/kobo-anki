package core

import (
	"log"
	"strconv"
	"strings"
)

// ConfigWarnings reports problems found while reading a config file, instead
// of the silent-ignore behaviour these parsers used to have.
//
// A mistyped key or an unparseable value used to leave the default in place
// with no output at all, which on a device whose state you cannot inspect is
// indistinguishable from the feature being broken - "I changed the setting
// and nothing happened" was the same symptom for a typo, an unsupported
// value, and a genuine bug. Every message names the file and line so the
// fix is obvious.
//
// Parsing still never fails: an unusable setting keeps its default and the
// rest of the file is applied. The goal is to make the fallback visible, not
// to turn config mistakes into a refusal to start.
type ConfigWarnings struct {
	Path  string
	Count int
}

func NewConfigWarnings(path string) *ConfigWarnings {
	return &ConfigWarnings{Path: path}
}

// UnknownKey reports a setting name this parser does not recognise - almost
// always a typo (dict_example for dict_examples) or a key put in the wrong
// one of the three config files.
func (w *ConfigWarnings) UnknownKey(line int, key string) {
	if w == nil {
		return
	}
	w.Count++
	log.Printf("%s:%d: unknown setting %q - ignored (typo, or does it belong in another conf file?)", w.Path, line, key)
}

// UnknownSection reports a section header this parser does not recognise.
// hint names the forms that are accepted, since a section header is not
// something a reader can guess at from the surrounding file the way a
// misspelled key can be compared with its neighbours.
func (w *ConfigWarnings) UnknownSection(line int, name, hint string) {
	if w == nil {
		return
	}
	w.Count++
	log.Printf("%s:%d: unknown section [%s] - ignored, along with everything in it (expected %s)", w.Path, line, name, hint)
}

// WrongScope reports a setting that exists but not here, naming where it
// belongs. Distinct from UnknownKey because the fix is different: nothing is
// misspelled, the line is just in the wrong part of the file, and "unknown
// setting" would send someone hunting for a typo that isn't there.
func (w *ConfigWarnings) WrongScope(line int, key, belongs string) {
	if w == nil {
		return
	}
	w.Count++
	log.Printf("%s:%d: %s= only means something %s - ignored here", w.Path, line, key, belongs)
}

// Removed reports a setting that used to exist and no longer does, naming
// what to write instead. A retired setting is worse than an unknown one: the
// config looks deliberate, so "unknown setting" reads as a typo rather than
// as "this moved". instead is a short phrase, not a sentence.
func (w *ConfigWarnings) Removed(line int, key, instead string) {
	if w == nil {
		return
	}
	w.Count++
	log.Printf("%s:%d: %s= has been removed - %s", w.Path, line, key, instead)
}

// BadValue reports a value that could not be parsed as the type the setting
// needs; want describes that type in plain words ("a whole number").
func (w *ConfigWarnings) BadValue(line int, key, val, want string) {
	if w == nil {
		return
	}
	w.Count++
	log.Printf("%s:%d: %s=%q is not %s - keeping the default", w.Path, line, key, val, want)
}

// Int/Float/Bool parse a value or report it, returning ok=false when the
// caller should leave its default alone. They return a value rather than
// assigning through a pointer so the same call works for both plain fields
// and the *T "unset means inherit" override structs.
func (w *ConfigWarnings) Int(line int, key, val string) (int, bool) {
	v, err := strconv.Atoi(val)
	if err != nil {
		w.BadValue(line, key, val, "a whole number")
		return 0, false
	}
	return v, true
}

func (w *ConfigWarnings) Float(line int, key, val string) (float64, bool) {
	v, err := strconv.ParseFloat(val, 64)
	if err != nil {
		w.BadValue(line, key, val, "a number")
		return 0, false
	}
	return v, true
}

// Bool accepts true/false/1/0/yes/no/on/off. The previous `val == "true"`
// idiom quietly treated everything else - including "yes" and "TRUE" - as
// false, which is exactly the kind of silent surprise this type exists to
// remove.
func (w *ConfigWarnings) Bool(line int, key, val string) (bool, bool) {
	switch strings.ToLower(val) {
	case "true", "1", "yes", "on":
		return true, true
	case "false", "0", "no", "off":
		return false, true
	}
	w.BadValue(line, key, val, "true or false")
	return false, false
}

// Summary logs a single closing line if anything was ignored, so a problem
// is still visible when the individual warnings have scrolled past.
func (w *ConfigWarnings) Summary() {
	if w == nil || w.Count == 0 {
		return
	}
	log.Printf("%s: %d setting(s) ignored - see the warnings above", w.Path, w.Count)
}
