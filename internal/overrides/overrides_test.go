package overrides

import (
	"errors"
	"testing"

	"github.com/dotwaffle/beamers/internal/auth"
)

func TestTechnicalDifficultiesRejectsDurationBeforeConversion(t *testing.T) {
	service := &Service{}
	_, err := service.ActivateTechnicalDifficulties(
		t.Context(),
		auth.Account{},
		TechnicalDifficultiesInput{
			EventID: 1, TargetGroupKey: "crew",
			DurationSeconds: int(^uint(0) >> 1),
		},
	)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("large Technical Difficulties duration error = %v", err)
	}
}
