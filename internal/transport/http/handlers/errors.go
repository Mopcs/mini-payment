package handlers

import "net/http"

// Named responses for the cases that repeat across handlers. They keep the
// error contract in one place instead of spelling out status and message at
// every call site.

func badRequest(w http.ResponseWriter, code, message string) {
	writeError(w, http.StatusBadRequest, code, message)
}

func conflict(w http.ResponseWriter, code, message string) {
	writeError(w, http.StatusConflict, code, message)
}

func unprocessable(w http.ResponseWriter, code, message string) {
	writeError(w, http.StatusUnprocessableEntity, code, message)
}

func unauthorized(w http.ResponseWriter) {
	writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
}

func walletNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "wallet_not_found", "wallet was not found")
}

func internalError(w http.ResponseWriter) {
	writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
}
