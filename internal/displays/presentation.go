package displays

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/a-h/templ"
)

func displayPageClass(snapshot Snapshot) string {
	theme := snapshot.Composition.Theme
	return strings.Join([]string{
		"display-view",
		"display-layout-" + snapshot.Composition.Layout.Key,
		"display-font-" + theme.Font,
		"display-background-" + theme.Background,
		"display-transition-" + theme.Transition,
	}, " ")
}

func displayBranding(snapshot Snapshot) string {
	if snapshot.Composition.Theme.Branding != "" {
		return snapshot.Composition.Theme.Branding
	}
	if snapshot.EventName != "" {
		return snapshot.EventName
	}
	return "Beamers"
}

func displayPageTitle(snapshot Snapshot) string {
	switch {
	case snapshot.Standby:
		return snapshot.Display.Name + " · Standby"
	case snapshot.ViewKey == "location-signage":
		return snapshot.LocationName + " · Location Signage"
	case snapshot.ViewKey == "event-overview":
		return snapshot.Display.Name + " · Event Overview"
	default:
		return snapshot.Display.Name + " · " + snapshot.EventName
	}
}

func displayPersistent(persistent bool) string {
	return strconv.FormatBool(persistent)
}

func displayNowNext(sessions []Session) []Session {
	result := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		if session.Lifecycle != "Canceled" {
			result = append(result, session)
		}
	}
	return result
}

func displayThemeStyle(snapshot Snapshot) templ.SafeCSS {
	theme := snapshot.Composition.Theme
	alpha := theme.ScrimOpacity * 255 / 100
	// Every interpolated value passed displayviews validation before the
	// Snapshot was created. Keeping the complete declaration here prevents
	// Event content from becoming CSS syntax.
	return templ.SafeCSS(fmt.Sprintf(
		"--display-foreground:%s;--display-background:%s;--display-accent:%s;"+
			"--display-scrim-layer:%s%02x",
		theme.ForegroundColor,
		theme.BackgroundColor,
		theme.AccentColor,
		theme.ScrimColor,
		alpha,
	))
}
