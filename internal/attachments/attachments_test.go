package attachments

import (
	"errors"
	"testing"

	"github.com/dotwaffle/beamers/internal/auth"
)

func TestReadVersionRequiresEventGrantForAdministrator(t *testing.T) {
	service := &Service{}
	_, _, err := service.ReadVersion(
		t.Context(),
		auth.Account{Administrator: true},
		1,
		1,
	)
	if !errors.Is(err, ErrUploadTargetNotFound) {
		t.Fatalf("Administrator without Event Grant error = %v, want not found", err)
	}
}
