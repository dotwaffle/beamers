package server

import (
	"fmt"
	"net/http"
	"strings"
)

const clientBuildHeader = "X-Beamers-Build"

func requireCompatibleClientBuild(buildVersion string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !crewClientPath(request.URL.Path) {
			next.ServeHTTP(response, request)
			return
		}
		response.Header().Set(clientBuildHeader, buildVersion)
		provided := request.Header.Get(clientBuildHeader)
		missingBrowserBuild := provided == "" &&
			request.Header.Get("Sec-Fetch-Site") != "" &&
			!strings.HasPrefix(
				request.Header.Get("Content-Type"),
				"application/x-www-form-urlencoded",
			)
		if mutationMethod(request.Method) &&
			(provided != "" && provided != buildVersion || missingBrowserBuild) {
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprintln(response, `{"code":"reload_required","message":"reload required"}`)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func crewClientPath(path string) bool {
	return strings.HasPrefix(path, "/admin/") ||
		strings.HasPrefix(path, "/beamers.activation.") ||
		strings.HasPrefix(path, "/beamers.rundown.") ||
		strings.HasPrefix(path, "/beamers.session.") ||
		strings.HasPrefix(path, "/beamers.program.")
}

func mutationMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}
