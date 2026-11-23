package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/imroc/req/v3"
)

// PrivacyClientFactory creates an HTTP client for privacy API calls.
// Injected from repository layer to avoid import cycles.
type PrivacyClientFactory func(proxyURL string) (*req.Client, error)

const (
	openAISettingsURL = "https://chatgpt.com/backend-api/settings/account_user_setting"

	PrivacyModeTrainingOff = "training_off"
	PrivacyModeFailed      = "training_set_failed"
	PrivacyModeCFBlocked   = "training_set_cf_blocked"
)

func shouldSkipOpenAIPrivacyEnsure(extra map[string]any) bool {
	if extra == nil {
		return false
	}
	raw, ok := extra["privacy_mode"]
	if !ok {
		return false
	}
	mode, _ := raw.(string)
	mode = strings.TrimSpace(mode)
	return mode != PrivacyModeFailed && mode != PrivacyModeCFBlocked
}

// disableOpenAITraining calls ChatGPT settings API to turn off "Improve the model for everyone".
// Returns privacy_mode value: "training_off" on success, "cf_blocked" / "failed" on failure.
func disableOpenAITraining(ctx context.Context, clientFactory PrivacyClientFactory, accessToken, proxyURL string) string {
	if accessToken == "" || clientFactory == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client, err := clientFactory(proxyURL)
	if err != nil {
		slog.Warn("openai_privacy_client_error")
		return PrivacyModeFailed
	}

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Origin", "https://chatgpt.com").
		SetHeader("Referer", "https://chatgpt.com/").
		SetHeader("Accept", "application/json").
		SetHeader("sec-fetch-mode", "cors").
		SetHeader("sec-fetch-site", "same-origin").
		SetHeader("sec-fetch-dest", "empty").
		SetQueryParam("feature", "training_allowed").
		SetQueryParam("value", "false").
		Patch(openAISettingsURL)

	if err != nil {
		slog.Warn("openai_privacy_request_error")
		return PrivacyModeFailed
	}

	if resp.StatusCode == 403 || resp.StatusCode == 503 {
		body := resp.String()
		if isCloudflareChallengeResponse(resp.Header.Get("cf-mitigated"), body) {
			slog.Warn("openai_privacy_cf_blocked", "status", resp.StatusCode)
			return PrivacyModeCFBlocked
		}
	}

	if !resp.IsSuccessState() {
		slog.Warn("openai_privacy_failed", "status", resp.StatusCode)
		return PrivacyModeFailed
	}

	slog.Info("openai_privacy_training_disabled")
	return PrivacyModeTrainingOff
}

// isCloudflareChallengeResponse 判断 chatgpt.com 返回的是否为 Cloudflare 质询/拦截页。
// 优先看 cf-mitigated 响应头（质询时为 "challenge"），再回退到正文关键字。
func isCloudflareChallengeResponse(cfMitigated, body string) bool {
	if strings.EqualFold(strings.TrimSpace(cfMitigated), "challenge") {
		return true
	}
	return strings.Contains(body, "cloudflare") || strings.Contains(body, "cf-") || strings.Contains(body, "Just a moment")
}

// ChatGPTAccountInfo 从 chatgpt.com/backend-api/accounts/check 获取的账号信息
type ChatGPTAccountInfo struct {
	PlanType string
	Email    string
	// AccountID 是本条信息所属账号的标识（优先取 account.account_id，否则取 accounts
	// 的 map key）。accounts/check 是多账号/工作区端点，调用方需要据此判断拿到的
	// plan_type / expires_at 到底属于个人账号还是某个 workspace。
	AccountID             string
	SubscriptionExpiresAt string // entitlement.expires_at (RFC3339)
}

const (
	chatGPTAccountsCheckURL = "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27"
	chatGPTSubscriptionsURL = "https://chatgpt.com/backend-api/subscriptions"
)

// fetchChatGPTAccountInfo calls ChatGPT backend-api to get account info (plan_type, etc.).
// Used as fallback when id_token doesn't contain these fields (e.g., Mobile RT).
// orgID is used to match the correct account when multiple accounts exist (e.g., personal + team).
// Returns nil on any failure (best-effort, non-blocking).
func fetchChatGPTAccountInfo(ctx context.Context, clientFactory PrivacyClientFactory, accessToken, proxyURL, orgID, preferredAccountID string) *ChatGPTAccountInfo {
	if accessToken == "" || clientFactory == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client, err := clientFactory(proxyURL)
	if err != nil {
		slog.Debug("chatgpt_account_check_client_error")
		return nil
	}

	var result map[string]any
	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Origin", "https://chatgpt.com").
		SetHeader("Referer", "https://chatgpt.com/").
		SetHeader("Accept", "application/json").
		SetSuccessResult(&result).
		Get(chatGPTAccountsCheckURL)

	if err != nil {
		slog.Warn("chatgpt_account_check_request_error")
		return nil
	}

	if !resp.IsSuccessState() {
		slog.Warn("chatgpt_account_check_failed", "status", resp.StatusCode, "cf_challenge", isCloudflareChallengeResponse(resp.Header.Get("cf-mitigated"), resp.String()))
		return nil
	}

	accounts, ok := result["accounts"].(map[string]any)
	if !ok {
		slog.Debug("chatgpt_account_check_no_accounts")
		return nil
	}

	info := selectChatGPTAccountInfo(accounts, orgID, preferredAccountID, time.Now())
	if info == nil {
		slog.Debug("chatgpt_account_check_no_plan_type")
		return nil
	}

	slog.Info("chatgpt_account_check_success", "has_plan_type", info.PlanType != "", "has_subscription_expiry", info.SubscriptionExpiresAt != "")
	return info
}

func selectChatGPTAccountInfo(accounts map[string]any, orgID, preferredAccountID string, now time.Time) *ChatGPTAccountInfo {
	preferredAccountID = strings.TrimSpace(preferredAccountID)
	var preferred, org, defaultAccount, paid, first *ChatGPTAccountInfo
	for key, raw := range accounts {
		acct, _ := raw.(map[string]any)
		if !isUsableChatGPTAccountCandidate(acct, now) {
			continue
		}
		info := &ChatGPTAccountInfo{}
		fillAccountInfo(info, acct, key)
		// When the token provides the plan, look for its own entitlement even
		// if poid points at a different default workspace.
		if preferredAccountID != "" && strings.EqualFold(preferredAccountID, info.AccountID) {
			if info.SubscriptionExpiresAt != "" {
				return info
			}
			if info.PlanType != "" {
				preferred = info
			}
		}
		if info.PlanType == "" {
			continue
		}
		if orgID != "" && key == orgID {
			org = info
		}
		account, _ := acct["account"].(map[string]any)
		if isDefault, _ := account["is_default"].(bool); isDefault {
			defaultAccount = info
		}
		if paid == nil && !strings.EqualFold(info.PlanType, "free") {
			paid = info
		}
		if first == nil {
			first = info
		}
	}
	for _, info := range []*ChatGPTAccountInfo{preferred, org, defaultAccount, paid, first} {
		if info != nil {
			return info
		}
	}
	return nil
}

// fetchChatGPTSubscriptionExpiresAt reads active_until for the specified account.
func fetchChatGPTSubscriptionExpiresAt(ctx context.Context, clientFactory PrivacyClientFactory, accessToken, proxyURL, accountID string) string {
	accountID = strings.TrimSpace(accountID)
	if accessToken == "" || accountID == "" || clientFactory == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client, err := clientFactory(proxyURL)
	if err != nil {
		slog.Debug("chatgpt_subscription_client_error")
		return ""
	}

	var result struct {
		ActiveUntil string `json:"active_until"`
	}
	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("ChatGPT-Account-ID", accountID).
		SetHeader("Origin", "https://chatgpt.com").
		SetHeader("Referer", "https://chatgpt.com/").
		SetHeader("Accept", "application/json").
		SetSuccessResult(&result).
		SetQueryParam("account_id", accountID).
		Get(chatGPTSubscriptionsURL)
	if err != nil {
		slog.Warn("chatgpt_subscription_request_error")
		return ""
	}
	if !resp.IsSuccessState() {
		slog.Warn("chatgpt_subscription_failed", "status", resp.StatusCode, "cf_challenge", isCloudflareChallengeResponse(resp.Header.Get("cf-mitigated"), resp.String()))
		return ""
	}

	activeUntil := strings.TrimSpace(result.ActiveUntil)
	if activeUntil == "" {
		slog.Debug("chatgpt_subscription_no_active_until")
		return ""
	}
	if validChatGPTSubscriptionExpiry(activeUntil) == "" {
		slog.Debug("chatgpt_subscription_bad_active_until")
		return ""
	}

	slog.Info("chatgpt_subscription_success")
	return activeUntil
}

// fillAccountInfo 从单个 account 对象中提取 plan_type 和 subscription_expires_at。
// fallbackID 是该对象在 accounts 里的 map key，用于 account.account_id 缺失时兜底。
func fillAccountInfo(info *ChatGPTAccountInfo, acct map[string]any, fallbackID string) {
	info.PlanType = extractPlanType(acct)
	info.SubscriptionExpiresAt = extractEntitlementExpiresAt(acct)
	info.AccountID = chatGPTAccountObjectID(acct, fallbackID)
}

// chatGPTAccountObjectID 取单个 account 对象的账号标识。
// accounts 的 map key 有时是 "default" 这类别名，所以优先读 account.account_id。
func chatGPTAccountObjectID(acct map[string]any, fallbackID string) string {
	if account, ok := acct["account"].(map[string]any); ok {
		if id, ok := account["account_id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return strings.TrimSpace(fallbackID)
}

// extractPlanType 从单个 account 对象中提取 plan_type
func extractPlanType(acct map[string]any) string {
	if account, ok := acct["account"].(map[string]any); ok {
		if planType, ok := account["plan_type"].(string); ok && planType != "" {
			return planType
		}
	}
	if entitlement, ok := acct["entitlement"].(map[string]any); ok {
		if subPlan, ok := entitlement["subscription_plan"].(string); ok && subPlan != "" {
			return subPlan
		}
	}
	return ""
}

func isUsableChatGPTAccountCandidate(acct map[string]any, now time.Time) bool {
	if acct == nil || hasChatGPTAccountDeactivatedMarker(acct) {
		return false
	}
	if account, ok := acct["account"].(map[string]any); ok && hasChatGPTAccountDeactivatedMarker(account) {
		return false
	}

	expiresAt := extractEntitlementExpiresAt(acct)
	if expiresAt == "" {
		return true
	}
	expiry, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return true
	}
	return expiry.After(now)
}

func hasChatGPTAccountDeactivatedMarker(obj map[string]any) bool {
	for _, key := range []string{"deactivated", "is_deactivated", "disabled", "is_disabled"} {
		if value, ok := obj[key].(bool); ok && value {
			return true
		}
	}
	for _, key := range []string{"deactivated_at", "disabled_at", "deleted_at"} {
		if value, ok := obj[key].(string); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	for _, key := range []string{"status", "state"} {
		value, _ := obj[key].(string)
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "deactivated", "disabled", "deleted", "inactive", "suspended":
			return true
		}
	}
	return false
}

// extractEntitlementExpiresAt 从 entitlement 中提取 expires_at。
// 预期为 RFC3339 字符串格式，如 "2026-05-02T20:32:12+00:00"。
func extractEntitlementExpiresAt(acct map[string]any) string {
	entitlement, ok := acct["entitlement"].(map[string]any)
	if !ok {
		return ""
	}
	ea, _ := entitlement["expires_at"].(string)
	return validChatGPTSubscriptionExpiry(ea)
}

func validChatGPTSubscriptionExpiry(raw string) string {
	value := strings.TrimSpace(raw)
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return ""
	}
	return value
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("...(%d more)", len(s)-n)
}
