package results

import (
	"context"

	"github.com/dotwaffle/beamers/internal/store"
)

// PublicArtifact is one frozen released Results representation set.
type PublicArtifact struct {
	Revision int
	HTML     string
	Text     string
	JSON     string
}

// PublicArtifact returns a released Results revision without crew authority.
func (service *Service) PublicArtifact(
	ctx context.Context,
	eventID int,
	scope PublicationScope,
	scopeSessionID int,
	revision int,
) (PublicArtifact, bool, error) {
	if eventID <= 0 || scopeSessionID <= 0 || revision < 0 ||
		(scope != PublicationScopePrizegiving &&
			scope != PublicationScopeStandalone &&
			scope != PublicationScopeEventAwards &&
			scope != PublicationScopeEvent) {
		return PublicArtifact{}, false, ErrInvalidInput
	}
	storeScope := map[PublicationScope]store.ResultsPublicationScope{
		PublicationScopePrizegiving: store.ResultsPublicationPrizegiving,
		PublicationScopeStandalone:  store.ResultsPublicationStandalone,
		PublicationScopeEventAwards: store.ResultsPublicationEventAwards,
		PublicationScopeEvent:       store.ResultsPublicationEvent,
	}[scope]
	if revision == 0 {
		found, err := service.storage.LoadResultsPublication(
			ctx,
			eventID,
			storeScope,
			scopeSessionID,
		)
		if err != nil {
			return PublicArtifact{}, false, err
		}
		if found.Revision == 0 {
			return PublicArtifact{}, false, nil
		}
		return publicArtifact(found), true, nil
	}
	found, exists, err := service.storage.LoadResultsPublicationRevision(
		ctx,
		eventID,
		storeScope,
		scopeSessionID,
		revision,
	)
	if err != nil {
		return PublicArtifact{}, false, err
	}
	if !exists {
		return PublicArtifact{}, false, nil
	}
	return publicArtifact(found), true, nil
}

func publicArtifact(found store.ResultsPublication) PublicArtifact {
	return PublicArtifact{
		Revision: found.Revision,
		HTML:     found.RenderedHTML,
		Text:     found.RenderedText,
		JSON:     found.RenderedJSON,
	}
}
