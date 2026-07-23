package store

import (
	"context"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/display"
	"github.com/dotwaffle/beamers/ent/displaycredential"
)

var (
	// ErrDisplayAcknowledgmentRegression means a Display reported an older cursor.
	ErrDisplayAcknowledgmentRegression = errors.New("Display acknowledgment regressed")
	// ErrDisplayAcknowledgmentConflict means one cursor was reused for different state.
	ErrDisplayAcknowledgmentConflict = errors.New("Display acknowledgment cursor conflicts")
)

// DisplayAcknowledgment is the latest state one Display reports applying.
type DisplayAcknowledgment struct {
	DisplayID                    int
	ProtocolVersion              string
	AssetVersion                 string
	StreamID                     string
	StreamPosition               int64
	ActiveEventID                int
	ActivationGeneration         int
	PublishedRevision            int
	AppliedAt                    time.Time
	AppliedStandby               bool
	ClockOffsetMilliseconds      int64
	ClockUncertaintyMilliseconds int64
	RendererUnstable             bool
}

// RecordDisplayAcknowledgment atomically advances one authenticated Display's applied state.
func (installationStore *SQLite) RecordDisplayAcknowledgment(
	ctx context.Context,
	credentialHash string,
	applied DisplayAcknowledgment,
) (DisplayAcknowledgment, error) {
	internalContext := systemContext(ctx)
	transaction, err := installationStore.client.Tx(internalContext)
	if err != nil {
		return DisplayAcknowledgment{}, opaqueError("begin Display acknowledgment", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	credential, err := transaction.DisplayCredential.Query().Where(
		displaycredential.TokenHashEQ(credentialHash),
		displaycredential.RevokedAtIsNil(),
	).WithDisplay().Only(internalContext)
	if ent.IsNotFound(err) {
		return DisplayAcknowledgment{}, ErrDisplayCredential
	}
	if err != nil {
		return DisplayAcknowledgment{}, opaqueError("authenticate Display acknowledgment", err)
	}
	found := credential.Edges.Display
	if found == nil {
		return DisplayAcknowledgment{}, opaqueError(
			"load Display acknowledgment owner",
			errors.New("missing Display"),
		)
	}
	applied.DisplayID = found.ID
	if found.AppliedStreamID == applied.StreamID {
		switch {
		case found.AppliedStreamPosition > applied.StreamPosition:
			return DisplayAcknowledgment{}, ErrDisplayAcknowledgmentRegression
		case found.AppliedStreamPosition == applied.StreamPosition:
			current := acknowledgment(found)
			if !sameAppliedState(current, applied) {
				return DisplayAcknowledgment{}, ErrDisplayAcknowledgmentConflict
			}
			if sameAcknowledgment(current, applied) {
				return current, nil
			}
		}
	}
	updated, err := transaction.Display.Update().Where(
		display.IDEQ(found.ID),
		display.Or(
			display.AppliedStreamIDNEQ(applied.StreamID),
			display.AppliedStreamPositionLT(applied.StreamPosition),
			display.And(
				display.AppliedStreamIDEQ(applied.StreamID),
				display.AppliedStreamPositionEQ(applied.StreamPosition),
			),
		),
	).
		SetAppliedProtocolVersion(applied.ProtocolVersion).
		SetAppliedAssetVersion(applied.AssetVersion).
		SetAppliedStreamID(applied.StreamID).
		SetAppliedStreamPosition(applied.StreamPosition).
		SetAppliedActiveEventID(applied.ActiveEventID).
		SetAppliedActivationGeneration(applied.ActivationGeneration).
		SetAppliedPublishedRevision(applied.PublishedRevision).
		SetAppliedStandby(applied.AppliedStandby).
		SetClockOffsetMilliseconds(applied.ClockOffsetMilliseconds).
		SetClockUncertaintyMilliseconds(applied.ClockUncertaintyMilliseconds).
		SetRendererUnstable(applied.RendererUnstable).
		SetAppliedAt(applied.AppliedAt).
		Save(internalContext)
	if err != nil {
		return DisplayAcknowledgment{}, opaqueError("record Display acknowledgment", err)
	}
	if updated != 1 {
		return DisplayAcknowledgment{}, ErrDisplayAcknowledgmentConflict
	}
	if err := transaction.Commit(); err != nil {
		return DisplayAcknowledgment{}, opaqueError("commit Display acknowledgment", err)
	}
	return applied, nil
}

func acknowledgment(found *ent.Display) DisplayAcknowledgment {
	result := DisplayAcknowledgment{
		DisplayID: found.ID, ProtocolVersion: found.AppliedProtocolVersion,
		AssetVersion: found.AppliedAssetVersion,
		StreamID:     found.AppliedStreamID, StreamPosition: found.AppliedStreamPosition,
		ActiveEventID:                found.AppliedActiveEventID,
		ActivationGeneration:         found.AppliedActivationGeneration,
		PublishedRevision:            found.AppliedPublishedRevision,
		AppliedStandby:               found.AppliedStandby,
		ClockOffsetMilliseconds:      found.ClockOffsetMilliseconds,
		ClockUncertaintyMilliseconds: found.ClockUncertaintyMilliseconds,
		RendererUnstable:             found.RendererUnstable,
	}
	if found.AppliedAt != nil {
		result.AppliedAt = *found.AppliedAt
	}
	return result
}

func sameAcknowledgment(first, second DisplayAcknowledgment) bool {
	return first.DisplayID == second.DisplayID &&
		sameAppliedState(first, second) &&
		first.ClockOffsetMilliseconds == second.ClockOffsetMilliseconds &&
		first.ClockUncertaintyMilliseconds == second.ClockUncertaintyMilliseconds &&
		first.RendererUnstable == second.RendererUnstable
}

func sameAppliedState(first, second DisplayAcknowledgment) bool {
	return first.ProtocolVersion == second.ProtocolVersion &&
		first.AssetVersion == second.AssetVersion &&
		first.StreamID == second.StreamID &&
		first.StreamPosition == second.StreamPosition &&
		first.ActiveEventID == second.ActiveEventID &&
		first.ActivationGeneration == second.ActivationGeneration &&
		first.PublishedRevision == second.PublishedRevision &&
		first.AppliedStandby == second.AppliedStandby
}
