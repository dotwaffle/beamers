package store

import (
	"context"
	"errors"
	"fmt"
)

// opaqueError hides storage error detail behind a stable action label, while
// still letting callers distinguish context cancellation and deadlines. It is
// used package-wide, not just by the Account concerns it originated beside.
func opaqueError(action string, err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%s: %w", action, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %w", action, context.DeadlineExceeded)
	}
	return errors.New(action + ": " + err.Error())
}
