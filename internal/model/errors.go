package model

import "fmt"

// AppError represents a structured API error.
type AppError struct {
	HTTPStatus int
	Code       string
	Message    string
	Details    map[string]any
}

func (e *AppError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func ErrInvalidInput(msg string) *AppError {
	return &AppError{HTTPStatus: 400, Code: "INVALID_INPUT", Message: msg}
}

func ErrInvalidCIDR(msg string) *AppError {
	return &AppError{HTTPStatus: 400, Code: "INVALID_CIDR", Message: msg}
}

func ErrUnauthenticated() *AppError {
	return &AppError{HTTPStatus: 401, Code: "UNAUTHENTICATED", Message: "missing or invalid credentials"}
}

func ErrPermissionDenied(msg string) *AppError {
	return &AppError{HTTPStatus: 403, Code: "PERMISSION_DENIED", Message: msg}
}

func ErrNotFound(resource string) *AppError {
	return &AppError{HTTPStatus: 404, Code: "NOT_FOUND", Message: fmt.Sprintf("%s not found", resource)}
}

func ErrCIDROverlap(msg string) *AppError {
	return &AppError{HTTPStatus: 409, Code: "CIDR_OVERLAP", Message: msg}
}

func ErrDuplicatePeering() *AppError {
	return &AppError{HTTPStatus: 409, Code: "DUPLICATE_PEERING", Message: "peering already exists between these VPCs"}
}

func ErrHasActivePeerings() *AppError {
	return &AppError{HTTPStatus: 409, Code: "HAS_ACTIVE_PEERINGS", Message: "cannot delete VPC with active peerings"}
}

func ErrInvalidState(msg string) *AppError {
	return &AppError{HTTPStatus: 409, Code: "INVALID_STATE", Message: msg}
}

func ErrConflict(msg string) *AppError {
	return &AppError{HTTPStatus: 409, Code: "CONFLICT", Message: msg}
}

func ErrQuotaExceeded(msg string) *AppError {
	return &AppError{HTTPStatus: 422, Code: "QUOTA_EXCEEDED", Message: msg}
}

func ErrVNIExhausted() *AppError {
	return &AppError{HTTPStatus: 422, Code: "VNI_EXHAUSTED", Message: "no VNIs available in target region"}
}

func ErrRateLimited() *AppError {
	return &AppError{HTTPStatus: 429, Code: "RATE_LIMITED", Message: "too many requests"}
}
