package needle

import (
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
		return fmt.Errorf("%w: negation detected", ErrValidation)
	}
	if len(response.Validation.Ungrounded) > 0 {
		return fmt.Errorf("%w: ungrounded fields: %s", ErrValidation, strings.Join(response.Validation.Ungrounded, ", "))
	}
	return nil
}
