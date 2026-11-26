package registry

import (
	"encoding/json"
	"net/http"
)

type errorBody struct {
	Errors []registryError `json:"errors"`
}

func writeJSON(w http.ResponseWriter, value any) error {
	return json.NewEncoder(w).Encode(value)
}

type registryError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`
}

func writeRegistryError(w http.ResponseWriter, status int, code, message string, detail any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Errors: []registryError{{Code: code, Message: message, Detail: detail}}})
}

func writeUnauthorized(w http.ResponseWriter, challenge string) {
	w.Header().Set("WWW-Authenticate", challenge)
	writeRegistryError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", nil)
}

func writeDenied(w http.ResponseWriter) {
	writeRegistryError(w, http.StatusForbidden, "DENIED", "requested access to the resource is denied", nil)
}

func writeNotFound(w http.ResponseWriter, code, message string) {
	writeRegistryError(w, http.StatusNotFound, code, message, nil)
}

func writeMethodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeRegistryError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed", nil)
}
