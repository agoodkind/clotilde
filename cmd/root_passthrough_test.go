package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPassthroughSkipsPostSessionTUI(t *testing.T) {
	t.Parallel()
	require.True(t, passthroughSkipsPostSessionTUI([]string{"api", "x"}))
	require.True(t, passthroughSkipsPostSessionTUI([]string{"--print", "q"}))
	require.True(t, passthroughSkipsPostSessionTUI([]string{"-p", "hello"}))
	require.True(t, passthroughSkipsPostSessionTUI([]string{"doctor"}))
	require.True(t, passthroughSkipsPostSessionTUI([]string{"update"}))
	require.True(t, passthroughSkipsPostSessionTUI([]string{"ps"}))
	require.False(t, passthroughSkipsPostSessionTUI([]string{".", "a"}))
	require.False(t, passthroughSkipsPostSessionTUI([]string{"exec", "x"}))
}
