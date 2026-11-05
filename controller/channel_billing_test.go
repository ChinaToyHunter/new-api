package controller

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVercelBalance(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       float64
		wantErr    bool
	}{
		{name: "remaining USD not lifetime spend", body: `{"balance":"95.50","total_used":"4.50"}`, want: 95.5},
		{name: "explicit zero", body: `{"balance":"0","total_used":"100"}`},
		{name: "negative credit balance", body: `{"balance":"-0.25"}`, want: -0.25},
		{name: "missing balance", body: `{"total_used":"10"}`, wantErr: true},
		{name: "null balance", body: `{"balance":null}`, wantErr: true},
		{name: "invalid amount", body: `{"balance":"private-upstream-value"}`, wantErr: true},
		{name: "not finite", body: `{"balance":"NaN"}`, wantErr: true},
		{name: "overflow", body: `{"balance":"1e999"}`, wantErr: true},
		{name: "upstream JSON error", body: `{"error":{"message":"private-upstream-value"}}`, wantErr: true},
		{name: "invalid JSON", body: `<html>private-upstream-value</html>`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupSub2APIBalanceTest(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/v1/credits", r.URL.Path)
				assert.Empty(t, r.URL.RawQuery)
				assert.Equal(t, "Bearer test-gateway-key", r.Header.Get("Authorization"))
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)
			baseURL := server.URL + "/v1/"
			channel := &model.Channel{Type: constant.ChannelTypeVercel, BaseURL: &baseURL, Key: "test-gateway-key", Balance: 42, BalanceUpdatedTime: 123}
			require.NoError(t, model.DB.Create(channel).Error)

			result, err := updateChannelBalance(channel)
			if tc.wantErr {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "private-upstream-value")
				assert.Equal(t, 42.0, channel.Balance)
				assert.Equal(t, int64(123), channel.BalanceUpdatedTime)
			} else {
				require.NoError(t, err)
				assert.InDelta(t, tc.want, result.Balance, 1e-9)
				assert.Empty(t, result.RawResponse)
			}
			assert.Equal(t, 1, calls)
			var stored model.Channel
			require.NoError(t, model.DB.First(&stored, channel.Id).Error)
			if tc.wantErr {
				assert.Equal(t, 42.0, stored.Balance)
				assert.Equal(t, int64(123), stored.BalanceUpdatedTime)
			} else {
				assert.InDelta(t, tc.want, stored.Balance, 1e-9)
				assert.Greater(t, stored.BalanceUpdatedTime, int64(123))
			}
		})
	}
}

func TestVercelBalanceTeamSelectionAndTransportFailures(t *testing.T) {
	for _, team := range []string{"team_example", "example-team"} {
		t.Run(team, func(t *testing.T) {
			setupSub2APIBalanceTest(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query()
				if strings.HasPrefix(team, "team_") {
					assert.Equal(t, team, query.Get("teamId"))
					assert.False(t, query.Has("slug"))
				} else {
					assert.Equal(t, team, query.Get("slug"))
					assert.False(t, query.Has("teamId"))
				}
				assert.Equal(t, "application/json", r.Header.Get("Accept"))
				_, _ = io.WriteString(w, `{"balance":"2.5"}`)
			}))
			t.Cleanup(server.Close)
			baseURL := server.URL + "/v4/ai"
			headerOverride := fmt.Sprintf(`{"x-vercel-ai-gateway-team":%q}`, team)
			channel := &model.Channel{Type: constant.ChannelTypeVercel, BaseURL: &baseURL, Key: "test-gateway-key", HeaderOverride: &headerOverride}
			require.NoError(t, model.DB.Create(channel).Error)
			result, err := updateChannelBalance(channel)
			require.NoError(t, err)
			assert.Equal(t, 2.5, result.Balance)
		})
	}
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: "private-upstream-value"},
		{name: "missing scope", status: http.StatusForbidden, body: "private-upstream-value"},
		{name: "gateway error", status: http.StatusBadGateway, body: "private-upstream-value"},
		{name: "redirect", status: http.StatusTemporaryRedirect},
		{name: "oversized", status: http.StatusOK, body: strings.Repeat(" ", maxAdvancedCustomBalanceResponseBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupSub2APIBalanceTest(t)
			redirectCalls := 0
			sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirectCalls++
				_, _ = io.WriteString(w, `{"balance":"0"}`)
			}))
			t.Cleanup(sink.Close)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", sink.URL)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)
			baseURL := server.URL
			channel := &model.Channel{Type: constant.ChannelTypeVercel, BaseURL: &baseURL, Key: "test-gateway-key", Balance: 42, BalanceUpdatedTime: 123}
			require.NoError(t, model.DB.Create(channel).Error)
			_, err := updateChannelBalance(channel)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private-upstream-value")
			assert.NotContains(t, err.Error(), channel.Key)
			assert.Zero(t, redirectCalls, "billing redirects must not disclose credentials")
			var stored model.Channel
			require.NoError(t, model.DB.First(&stored, channel.Id).Error)
			assert.Equal(t, 42.0, stored.Balance)
			assert.Equal(t, int64(123), stored.BalanceUpdatedTime)
		})
	}
}

func TestVercelBalanceDefaultURLAndNetworkError(t *testing.T) {
	setupSub2APIBalanceTest(t)
	client := service.GetHttpClient()
	if client == nil {
		client = http.DefaultClient
	}
	original := client.Transport
	t.Cleanup(func() { client.Transport = original })
	client.Transport = emailHTTPTransport(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, "https://ai-gateway.vercel.sh/v1/credits", req.URL.String())
		assert.Equal(t, "Bearer test-gateway-key", req.Header.Get("Authorization"))
		_, deadline := req.Context().Deadline()
		assert.True(t, deadline, "balance requests must have a deadline")
		return nil, errors.New("private-network-detail test-gateway-key")
	})
	channel := &model.Channel{Type: constant.ChannelTypeVercel, Key: "test-gateway-key", Balance: 42}
	require.NoError(t, model.DB.Create(channel).Error)
	_, err := updateChannelBalance(channel)
	require.EqualError(t, err, "Provider balance request failed")
	assert.Equal(t, 42.0, channel.Balance)
}

func TestDeepInfraBalanceRoutesExistingCompatibleChannels(t *testing.T) {
	setupSub2APIBalanceTest(t)
	client := service.GetHttpClient()
	if client == nil {
		client = http.DefaultClient
	}
	original := client.Transport
	t.Cleanup(func() { client.Transport = original })
	calls := 0
	client.Transport = emailHTTPTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, "https://api.deepinfra.com/v1/me?checklist=true", req.URL.String())
		assert.Equal(t, http.MethodGet, req.Method)
		assert.Equal(t, "Bearer test-deepinfra-key", req.Header.Get("Authorization"))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"checklist":{"stripe_balance":-20,"recent":2}}`))}, nil
	})
	for _, channelType := range []int{constant.ChannelTypeOpenAI, constant.ChannelTypeCustom} {
		for _, base := range []string{"https://api.deepinfra.com", "https://api.deepinfra.com/v1/openai/", "https://API.DEEPINFRA.COM:443/v1/openai/chat/completions"} {
			t.Run(fmt.Sprintf("%d/%s", channelType, base), func(t *testing.T) {
				channel := &model.Channel{Type: channelType, BaseURL: &base, Key: "test-deepinfra-key", Balance: 42}
				require.NoError(t, model.DB.Create(channel).Error)
				result, err := updateChannelBalance(channel)
				require.NoError(t, err)
				assert.Equal(t, 18.0, result.Balance)
				var stored model.Channel
				require.NoError(t, model.DB.First(&stored, channel.Id).Error)
				assert.Equal(t, 18.0, stored.Balance)
			})
		}
	}
	assert.Equal(t, 6, calls)
}

func TestDeepInfraBalanceResponseValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       float64
		wantErr    bool
	}{
		{name: "unbilled usage deducted in USD", body: `{"checklist":{"stripe_balance":-20,"recent":2.25}}`, want: 17.75},
		{name: "empty wallet", body: `{"checklist":{"stripe_balance":0,"recent":0}}`},
		{name: "money owed", body: `{"checklist":{"stripe_balance":3,"recent":1}}`, want: -4},
		{name: "missing checklist", body: `{}`, wantErr: true},
		{name: "missing unbilled usage", body: `{"checklist":{"stripe_balance":-20}}`, wantErr: true},
		{name: "null wallet", body: `{"checklist":{"stripe_balance":null,"recent":0}}`, wantErr: true},
		{name: "invalid number", body: `{"checklist":{"stripe_balance":"private-upstream-value","recent":0}}`, wantErr: true},
		{name: "sum overflow", body: `{"checklist":{"stripe_balance":1.7e308,"recent":1.7e308}}`, wantErr: true},
		{name: "JSON error", body: `{"detail":{"error":"private-upstream-value"}}`, wantErr: true},
		{name: "active scoped credits must not be treated as empty wallet", body: `{"checklist":{"stripe_balance":0,"recent":0,"scoped_credits":[{"remaining_cents":1000,"expired":false,"covers_everything":true}]}}`, wantErr: true},
		{name: "model-specific scoped credits are not universal cash", body: `{"checklist":{"stripe_balance":0,"recent":0,"scoped_credits":[{"remaining_cents":1000,"expired":false,"covers_everything":false}]}}`, wantErr: true},
		{name: "expired scoped credits do not inflate balance", body: `{"checklist":{"stripe_balance":-5,"recent":1,"scoped_credits":[{"remaining_cents":1000,"expired":true}]}}`, want: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupSub2APIBalanceTest(t)
			client := service.GetHttpClient()
			if client == nil {
				client = http.DefaultClient
			}
			original := client.Transport
			t.Cleanup(func() { client.Transport = original })
			client.Transport = emailHTTPTransport(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			baseURL := "https://api.deepinfra.com/v1/openai"
			channel := &model.Channel{Type: constant.ChannelTypeOpenAI, BaseURL: &baseURL, Key: "test-deepinfra-key", Balance: 42, BalanceUpdatedTime: 123}
			require.NoError(t, model.DB.Create(channel).Error)
			result, err := updateChannelBalance(channel)
			if tc.wantErr {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "private-upstream-value")
			} else {
				require.NoError(t, err)
				assert.InDelta(t, tc.want, result.Balance, 1e-9)
			}
			var stored model.Channel
			require.NoError(t, model.DB.First(&stored, channel.Id).Error)
			if tc.wantErr {
				assert.Equal(t, 42.0, stored.Balance)
				assert.Equal(t, int64(123), stored.BalanceUpdatedTime)
			} else {
				assert.InDelta(t, tc.want, stored.Balance, 1e-9)
			}
		})
	}
}

func TestDeepInfraBalanceDoesNotHijackOtherUpstreams(t *testing.T) {
	for _, baseURL := range []string{
		"https://api.openai.com", "https://relay.example/api.deepinfra.com",
		"https://api.deepinfra.com.relay.example", "http://api.deepinfra.com",
		"https://api.deepinfra.com:8443", "https://user@api.deepinfra.com",
		"https://api.deepinfra.com?key=private-query", "https://api.deepinfra.com#fragment",
	} {
		t.Run(baseURL, func(t *testing.T) {
			setupSub2APIBalanceTest(t)
			client := service.GetHttpClient()
			if client == nil {
				client = http.DefaultClient
			}
			original := client.Transport
			t.Cleanup(func() { client.Transport = original })
			client.Transport = emailHTTPTransport(func(req *http.Request) (*http.Response, error) {
				assert.NotEqual(t, "/v1/me", req.URL.Path, "only the exact official HTTPS origin uses DeepInfra billing")
				body := `{"has_payment_method":true,"hard_limit_usd":20}`
				if strings.HasSuffix(req.URL.Path, "/usage") {
					body = `{"total_usage":200}`
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			channel := &model.Channel{Type: constant.ChannelTypeOpenAI, BaseURL: &baseURL, Key: "test-relay-key"}
			require.NoError(t, model.DB.Create(channel).Error)
			_, err := updateChannelBalance(channel)
			require.NoError(t, err)
		})
	}
}

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
