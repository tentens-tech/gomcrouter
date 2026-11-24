package main

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/version"
)

func TestMain_Version(t *testing.T) {
	origArgs, origStdout := os.Args, os.Stdout
	t.Cleanup(func() { os.Args, os.Stdout = origArgs, origStdout })

	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Args = []string{"gomcrouter", "--version"}
	os.Stdout = w

	main()

	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	assert.Equal(t, "gomcrouter version "+version.Version+"\n", string(out))
}
