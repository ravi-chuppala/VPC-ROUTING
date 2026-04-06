package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/ravi-chuppala/vpc-routing/internal/model"
)

type SuccessResponse struct {
	Data      any    `json:"data"`
	RequestID string `json:"request_id"`
}

type ListResponse struct {
	Data       any         `json:"data"`
	Pagination *Pagination `json:"pagination,omitempty"`
	RequestID  string      `json:"request_id"`
}

type Pagination struct {
	NextPageToken string `json:"next_page_token,omitempty"`
	TotalCount    int    `json:"total_count"`
}

type ErrorResponse struct {
	Error     ErrorBody `json:"error"`
	RequestID string    `json:"request_id"`
}

type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeSuccess(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, SuccessResponse{
		Data:      data,
		RequestID: uuid.New().String(),
	})
}

func writeCreated(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusCreated, SuccessResponse{
		Data:      data,
		RequestID: uuid.New().String(),
	})
}

func writeList(w http.ResponseWriter, data any, total int, nextToken string) {
	writeJSON(w, http.StatusOK, ListResponse{
		Data: data,
		Pagination: &Pagination{
			NextPageToken: nextToken,
			TotalCount:    total,
		},
		RequestID: uuid.New().String(),
	})
}

func writeError(w http.ResponseWriter, err error) {
	appErr, ok := err.(*model.AppError)
	if !ok {
		appErr = &model.AppError{HTTPStatus: 500, Code: "INTERNAL", Message: err.Error()}
	}
	writeJSON(w, appErr.HTTPStatus, ErrorResponse{
		Error: ErrorBody{
			Code:    appErr.Code,
			Message: appErr.Message,
			Details: appErr.Details,
		},
		RequestID: uuid.New().String(),
	})
}

func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}
