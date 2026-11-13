package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

func openAIOAuthPrincipal(ctx context.Context) (string, string, error) {
	p := adminaccess.FromContext(ctx)
	if p == nil {
		return "", "", nil
	}
	if p.KeyID == "" || (!p.Policy.FullAccess && !slices.Contains(p.Policy.GlobalPermissions, "openai.oauth.authorize")) {
		return "", "", infraerrors.Forbidden("OPENAI_OAUTH_FORBIDDEN", "OAuth authorization is not permitted")
	}
	encoded, err := json.Marshal(p.Policy)
	if err != nil {
		return "", "", err
	}
	hash := sha256.Sum256(encoded)
	return p.KeyID, hex.EncodeToString(hash[:]), nil
}

func validateRestrictedOpenAIOAuthInput(ctx context.Context, proxyID *int64, redirectURI, platform string) error {
	if adminaccess.RestrictedFromContext(ctx) == nil {
		return nil
	}
	if proxyID != nil {
		return infraerrors.Forbidden("OPENAI_OAUTH_PROXY_FORBIDDEN", "Proxy overrides are not permitted for scoped OAuth")
	}
	if platform != "" && platform != PlatformOpenAI {
		return infraerrors.Forbidden("OPENAI_OAUTH_PLATFORM_FORBIDDEN", "Only OpenAI OAuth is permitted")
	}
	if redirectURI != "" && redirectURI != openai.DefaultRedirectURI && redirectURI != "http://127.0.0.1:1455/auth/callback" {
		return infraerrors.BadRequest("OPENAI_OAUTH_REDIRECT_FORBIDDEN", "Use the fixed loopback OAuth callback")
	}
	return nil
}

func validateOpenAIOAuthSessionOwner(ctx context.Context, session *openai.OAuthSession, input *OpenAIExchangeCodeInput) error {
	owner, hash, err := openAIOAuthPrincipal(ctx)
	if err != nil {
		return err
	}
	if owner != session.OwnerKeyID || hash != session.OwnerPolicyHash {
		return infraerrors.Forbidden("OPENAI_OAUTH_SESSION_OWNER_MISMATCH", "OAuth session does not belong to this authorization context")
	}
	if adminaccess.RestrictedFromContext(ctx) != nil {
		if err := validateRestrictedOpenAIOAuthInput(ctx, input.ProxyID, session.RedirectURI, PlatformOpenAI); err != nil {
			return err
		}
		if input.RedirectURI != "" && input.RedirectURI != session.RedirectURI {
			return infraerrors.Forbidden("OPENAI_OAUTH_SESSION_OVERRIDE_FORBIDDEN", "OAuth session callback cannot be changed")
		}
	}
	return nil
}
