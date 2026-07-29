package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displayviews"
)

func (handlers operationHandlers) enrollDisplay(
	request *http.Request,
	actor auth.Account,
) error {
	displayID, err := optionalOperationID(request.Form.Get("display_id"))
	if err != nil {
		return formValidationError("display_id", "Enter a positive Display ID.")
	}
	name := strings.TrimSpace(request.Form.Get("name"))
	if displayID == 0 && !displays.ValidDisplayName(name) {
		return formValidationError(
			"name",
			"Enter a Display name of at most 200 characters.",
		)
	}
	_, err = handlers.displays.ClaimEnrollment(
		request.Context(),
		actor,
		displays.ClaimInput{
			Code: request.Form.Get("code"), Name: name,
			DisplayID: displayID, CommandID: request.Form.Get("command_id"),
		},
	)
	return err
}

func (handlers operationHandlers) assignDisplay(
	request *http.Request,
	actor auth.Account,
	eventID int,
) error {
	displayID, err := requiredOperationID(request.Form.Get("display_id"))
	if err != nil {
		return err
	}
	locationID, err := requiredOperationID(request.Form.Get("location_id"))
	if err != nil {
		return formValidationError("location_id", "Choose a valid Display location.")
	}
	viewKey := strings.TrimSpace(request.Form.Get("view_key"))
	if !displayviews.IsNormal(viewKey) {
		return formValidationError("view_key", "Choose a valid Display view.")
	}
	displayGroupKeys := commaSeparatedValues(request.Form.Get("display_group_keys"))
	if !displays.ValidDisplayGroupKeys(displayGroupKeys) {
		return formValidationError(
			"display_group_keys",
			"Enter valid Display Group keys.",
		)
	}
	_, err = handlers.displays.Assign(
		request.Context(),
		actor,
		displays.AssignInput{
			DisplayID: displayID, EventID: eventID, LocationID: locationID,
			ViewKey:          viewKey,
			DisplayGroupKeys: displayGroupKeys,
			CommandID:        request.Form.Get("command_id"),
		},
	)
	return err
}

func requiredOperationID(value string) (int, error) {
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		return 0, errInvalidOperationInput
	}
	return id, nil
}

func optionalOperationID(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	return requiredOperationID(value)
}
