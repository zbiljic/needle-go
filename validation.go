package needle

import (
	"errors"
	"fmt"
	"strings"
)

// ValidateResponse checks only engine validation warnings, accepting absent metadata.
// It rejects negation or any ungrounded field path, including possible false positives;
// a matching number elsewhere in the prompt does not exempt a warning.
// Unflagged output still needs application validation.
func ValidateResponse(response Response) error {
	if response.Validation == nil {
		return nil
	}
	if response.Validation.Negation {
		return errors.New("needle: response validation: negation detected")
	}
	if len(response.Validation.Ungrounded) > 0 {
		return fmt.Errorf("needle: response validation: ungrounded fields: %s", strings.Join(response.Validation.Ungrounded, ", "))
	}
	return nil
}
