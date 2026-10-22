package controller

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetDeepSeekBalanceUSD(t *testing.T) {
	tests := []struct {
		name            string
		responseJSON    string
		usdExchangeRate float64
		want            float64
		wantErrContains string
	}{
		{
			name:            "prefers USD when USD precedes CNY",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"12.50"},{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: 7.3,
			want:            12.5,
		},
		{
			name:            "prefers USD when CNY precedes USD",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"},{"currency":"USD","total_balance":"12.50"}]}`,
			usdExchangeRate: 7.3,
			want:            12.5,
		},
		{
			name:            "converts CNY when USD is absent",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: 7.3,
			want:            10,
		},
		{
			name:            "converts CNY when USD balance is zero",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"0.00"},{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: 7.3,
			want:            10,
		},
		{
			name:            "converts CNY when zero USD follows CNY",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"},{"currency":"USD","total_balance":"0.00"}]}`,
			usdExchangeRate: 7.3,
			want:            10,
		},
		{
			name:            "returns zero when USD balance is zero and CNY is absent",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"0.00"}]}`,
			usdExchangeRate: 7.3,
			want:            0,
		},
		{
			name:            "returns error when USD and CNY are absent",
			responseJSON:    `{"balance_infos":[{"currency":"EUR","total_balance":"10.00"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "currency USD or CNY not found",
		},
		{
			name:            "returns USD parse error instead of falling back to CNY",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"invalid"},{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "invalid syntax",
		},
		{
			name:            "rejects NaN USD balance",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"NaN"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "USD balance must be finite",
		},
		{
			name:            "rejects negative USD balance",
			responseJSON:    `{"balance_infos":[{"currency":"USD","total_balance":"-1.00"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "USD balance must be non-negative",
		},
		{
			name:            "rejects positive infinity CNY balance",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"+Inf"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "CNY balance must be finite",
		},
		{
			name:            "rejects negative CNY balance",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"-7.30"}]}`,
			usdExchangeRate: 7.3,
			wantErrContains: "CNY balance must be non-negative",
		},
		{
			name:            "returns error for non-positive CNY exchange rate",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: 0,
			wantErrContains: "USD exchange rate must be greater than zero",
		},
		{
			name:            "rejects NaN CNY exchange rate",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: math.NaN(),
			wantErrContains: "USD exchange rate must be finite",
		},
		{
			name:            "rejects positive infinity CNY exchange rate",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"73.00"}]}`,
			usdExchangeRate: math.Inf(1),
			wantErrContains: "USD exchange rate must be finite",
		},
		{
			name:            "rejects CNY conversion overflow",
			responseJSON:    `{"balance_infos":[{"currency":"CNY","total_balance":"1.7976931348623157e+308"}]}`,
			usdExchangeRate: math.SmallestNonzeroFloat64,
			wantErrContains: "converted USD balance must be finite",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var response DeepSeekUsageResponse
			require.NoError(t, common.Unmarshal([]byte(test.responseJSON), &response))

			balance, err := getDeepSeekBalanceUSD(response, test.usdExchangeRate)
			if test.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.wantErrContains)
				return
			}

			require.NoError(t, err)
			assert.InDelta(t, test.want, balance, 1e-12)
		})
	}
}

func TestUpdateChannelNewAPIBalanceUsesTheSmallerRemainingQuota(t *testing.T) {
	for _, tc := range []struct {
		name    string
		usage   string
		want    float64
		wantErr string
	}{
		{name: "user quota below the key quota", usage: `{"code":true,"data":{"total_available":2000000,"unlimited_quota":false,"user_quota":{"total_available":500000}}}`, want: 0.5},
		{name: "key quota below the user quota", usage: `{"code":true,"data":{"total_available":500000,"unlimited_quota":false,"user_quota":{"total_available":2000000}}}`, want: 0.5},
		{name: "unlimited key uses the user quota", usage: `{"code":true,"data":{"total_available":0,"unlimited_quota":true,"user_quota":{"total_available":3000000}}}`, want: 3},
		{name: "key quota alone without user quota", usage: `{"code":true,"data":{"total_available":1500000,"unlimited_quota":false}}`, want: 1.5},
		{name: "unlimited key without user quota", usage: `{"code":true,"data":{"total_available":0,"unlimited_quota":true}}`, wantErr: "Allow querying user quota"},
		{name: "upstream rejects the key", usage: `{"success":false,"message":"invalid token"}`, wantErr: "invalid token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupSub2APIBalanceTest(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/usage/token/":
					assert.Equal(t, "Bearer sk-upstream", r.Header.Get("Authorization"))
					_, _ = w.Write([]byte(tc.usage))
				case "/api/status":
					assert.Empty(t, r.Header.Get("Authorization"), "the key is not sent to the public status endpoint")
					_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":1000000}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			baseURL := server.URL + "/"
			channel := &model.Channel{Type: constant.ChannelTypeNewAPI, BaseURL: &baseURL, Key: "sk-upstream", Name: "new-api-upstream"}
			require.NoError(t, model.DB.Create(channel).Error)

			result, err := updateChannelBalance(channel)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tc.want, result.Balance, 1e-9)
			assert.InDelta(t, tc.want, channel.Balance, 1e-9)
		})
	}
}

func TestUpdateCommandCodeCreditsReadsTheAccountWallet(t *testing.T) {
	for _, tc := range []struct {
		name      string
		whoami    string
		wantOrgID string
	}{
		{name: "personal account sends no organization", whoami: `{"user":{"id":"u_1"},"org":null}`},
		{name: "organization member reads the organization wallet", whoami: `{"user":{"id":"u_1"},"org":{"id":"org_1"}}`, wantOrgID: "org_1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupSub2APIBalanceTest(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer sk-command", r.Header.Get("Authorization"))
				assert.Contains(t, r.Header.Get("User-Agent"), "codex-tui/", "the official host rejects library user agents")
				switch r.URL.Path {
				case "/alpha/whoami":
					_, _ = w.Write([]byte(tc.whoami))
				case "/alpha/billing/credits":
					orgID, hasOrgID := r.URL.Query()["orgId"]
					if tc.wantOrgID == "" {
						assert.False(t, hasOrgID, "a personal account must not send orgId")
					} else {
						assert.Equal(t, []string{tc.wantOrgID}, orgID)
					}
					_, _ = w.Write([]byte(`{"credits":{"monthlyCredits":"12.5","purchasedCredits":3,"freeCredits":0.5},"windowLimits":{"limited":false}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			baseURL := "https://api.commandcode.ai/provider"
			channel := &model.Channel{Type: constant.ChannelTypeCommandCode, BaseURL: &baseURL, Key: "sk-command", Name: "command-code"}
			require.NoError(t, model.DB.Create(channel).Error)

			balance, err := updateCommandCodeCredits(channel, server.URL)
			require.NoError(t, err)
			assert.InDelta(t, 16.0, balance, 1e-9)
			assert.InDelta(t, 16.0, channel.Balance, 1e-9)
		})
	}

	t.Run("relay hosts never receive the billing request", func(t *testing.T) {
		setupSub2APIBalanceTest(t)
		baseURL := "https://relay.example/provider"
		channel := &model.Channel{Type: constant.ChannelTypeCommandCode, BaseURL: &baseURL, Key: "sk-command", Name: "command-code-relay"}
		require.NoError(t, model.DB.Create(channel).Error)
		_, err := updateChannelBalance(channel)
		require.ErrorContains(t, err, "official api.commandcode.ai endpoint")
	})
}

func TestCommandCodeInvalidWalletPreservesBalance(t *testing.T) {
	for _, tc := range []struct {
		name, whoami, credits string
	}{
		{"HTML identity", "<html>error</html>", `{"credits":{"monthlyCredits":0,"purchasedCredits":0,"freeCredits":0}}`},
		{"missing organization identity", `{}`, `{"credits":{"monthlyCredits":0,"purchasedCredits":0,"freeCredits":0}}`},
		{"invalid organization identity", `{"org":{}}`, `{"credits":{"monthlyCredits":0,"purchasedCredits":0,"freeCredits":0}}`},
		{"invalid credit string", `{"org":null}`, `{"credits":{"monthlyCredits":"not-a-number","purchasedCredits":0,"freeCredits":0}}`},
		{"non-finite credits", `{"org":null}`, `{"credits":{"monthlyCredits":"NaN","purchasedCredits":0,"freeCredits":0}}`},
		{"infinite credits", `{"org":null}`, `{"credits":{"monthlyCredits":"Inf","purchasedCredits":0,"freeCredits":0}}`},
		{"negative credits", `{"org":null}`, `{"credits":{"monthlyCredits":-1,"purchasedCredits":0,"freeCredits":0}}`},
		{"incomplete credits", `{"org":null}`, `{"credits":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupSub2APIBalanceTest(t)
			var creditCalls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/alpha/whoami" {
					_, _ = w.Write([]byte(tc.whoami))
					return
				}
				creditCalls++
				_, _ = w.Write([]byte(tc.credits))
			}))
			t.Cleanup(server.Close)
			base := "https://api.commandcode.ai/provider"
			ch := &model.Channel{Type: constant.ChannelTypeCommandCode, BaseURL: &base, Key: "test-key", Name: "invalid-wallet", Balance: 42}
			require.NoError(t, model.DB.Create(ch).Error)
			_, err := updateCommandCodeCredits(ch, server.URL)
			require.Error(t, err)
			assert.Equal(t, 42.0, ch.Balance)
			if tc.whoami != `{"org":null}` {
				assert.Zero(t, creditCalls, "unknown organization must not fall back to the personal wallet")
			}
		})
	}
}
