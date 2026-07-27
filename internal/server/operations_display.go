package server

import (
	"net/http"
	"strconv"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displays"
)

func (handlers operationHandlers) enrollDisplay(
	request *http.Request,
	actor auth.Account,
) error {
	displayID, err := optionalOperationID(request.Form.Get("display_id"))
	if err != nil {
		return err
	}
	_, err = handlers.displays.ClaimEnrollment(
		request.Context(),
		actor,
		displays.ClaimInput{
			Code: request.Form.Get("code"), Name: request.Form.Get("name"),
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
		return err
	}
	_, err = handlers.displays.Assign(
		request.Context(),
		actor,
		displays.AssignInput{
			DisplayID: displayID, EventID: eventID, LocationID: locationID,
			ViewKey:          request.Form.Get("view_key"),
			DisplayGroupKeys: commaSeparatedValues(request.Form.Get("display_group_keys")),
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
