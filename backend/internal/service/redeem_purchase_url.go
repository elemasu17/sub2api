package service

import (
	"net/url"
	"os"
	"strings"
)

// This URL is public configuration, never a user-specific authenticated URL.
func redeemPurchaseURLFromEnvironment() string {
	raw := strings.TrimSpace(os.Getenv("REDEEM_PURCHASE_URL"))
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || strings.ContainsAny(raw, "\\\r\n\t") {
		return ""
	}
	return raw
}
