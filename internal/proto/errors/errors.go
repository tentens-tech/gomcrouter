package errors

import "github.com/pkg/errors"

var (
	ErrMalformedRequest  = errors.New("malformed request")
	ErrMalformedResponse = errors.New("malformed response")
)
