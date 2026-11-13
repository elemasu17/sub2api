//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestNativeReleaseRejectsBinaryReplacementByDefault(t *testing.T) {
	for _, value := range []string{"", "false", "1", "TRUE"} {
		t.Run("env="+value, func(t *testing.T) {
			t.Setenv("ALLOW_OFFICIAL_BINARY_UPDATES", value)
			// A nil client verifies rejection occurs before network or filesystem work.
			svc := NewUpdateService(nil, nil, "0.2.21-aibj.1", "source")
			require.ErrorIs(t, svc.PerformUpdate(context.Background()), ErrOfficialBinaryUpdatesDisabled)
			require.ErrorIs(t, svc.Rollback(), ErrOfficialBinaryUpdatesDisabled)
			require.ErrorIs(t, svc.RollbackToVersion(context.Background(), "0.2.20"), ErrOfficialBinaryUpdatesDisabled)
		})
	}
}

func TestRedeemPurchasePublicSettingsValidateEnvironment(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""},
		{" https://shop.example.com/buy?channel=sub2api ", "https://shop.example.com/buy?channel=sub2api"},
		{"http://shop.example.com", ""},
		{"javascript:alert(1)", ""},
		{"//shop.example.com", ""},
		{"https:///missing-host", ""},
		{"https://user:password@shop.example.com", ""},
		{"https://shop.example.com\\evil", ""},
		{"https://shop.example.com/\nnext", ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Setenv("REDEEM_PURCHASE_URL", tc.input)
			svc := NewSettingService(&settingPublicRepoStub{values: map[string]string{}}, &config.Config{})
			settings, err := svc.GetPublicSettings(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.want, settings.RedeemPurchaseURL)
			injected, err := svc.GetPublicSettingsForInjection(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.want, injected.(*PublicSettingsInjectionPayload).RedeemPurchaseURL)
		})
	}
}
