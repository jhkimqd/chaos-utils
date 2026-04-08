// Package safeshell provides validation helpers that prevent shell injection
// when parameters are interpolated into sh -c command strings via fmt.Sprintf.
package safeshell

import (
	"fmt"
	"regexp"
)

// shellUnsafeChars matches characters that could break out of shell quoting
// or enable command injection in sh -c strings.
var shellUnsafeChars = regexp.MustCompile("['\"`$;|&(){}\\\\\\n\\r]")

// ValidateShellSafe returns an error if s contains characters that are unsafe
// for interpolation into shell commands via fmt.Sprintf.
func ValidateShellSafe(s string) error {
	if shellUnsafeChars.MatchString(s) {
		return fmt.Errorf("parameter %q contains unsafe shell characters", s)
	}
	return nil
}
