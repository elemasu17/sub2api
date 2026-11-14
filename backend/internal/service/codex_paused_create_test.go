package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPausedCodexCreatePreservesLegacyDefaults(t *testing.T) {
	for _, paused := range []bool{false, true} {
		input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, CreatePaused: paused, Credentials: map[string]any{"access_token": "fixture"}}
		account, err := buildAccountForCreate(input, map[string]any{})
		require.NoError(t, err)
		if paused {
			require.Equal(t, "inactive", account.Status)
			require.False(t, account.Schedulable)
		} else {
			require.Equal(t, StatusActive, account.Status)
			require.True(t, account.Schedulable)
		}
	}
}
