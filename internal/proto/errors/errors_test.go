package errors

import (
	"testing"

	pkgerrors "github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSentinelErrors(t *testing.T) {
	require.EqualError(t, ErrMalformedRequest, "malformed request")
	require.EqualError(t, ErrMalformedResponse, "malformed response")
	require.NotErrorIs(t, ErrMalformedRequest, ErrMalformedResponse)
}

func TestSentinelErrorsSurviveWrapping(t *testing.T) {
	for _, sentinel := range []error{ErrMalformedRequest, ErrMalformedResponse} {
		wrapped := pkgerrors.Wrap(sentinel, "decode")
		require.ErrorIs(t, wrapped, sentinel)
		assert.Equal(t, sentinel, pkgerrors.Cause(wrapped))
	}
}
