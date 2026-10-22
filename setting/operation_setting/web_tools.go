package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

// Hosted web tools modes. Native forwards hosted web search and page fetch to
// the upstream; gateway runs them in New API with the DeepSeek Search and
// Fetch APIs and answers the upstream model through function calls.
const (
	WebToolsModeNative  = "native"
	WebToolsModeGateway = "gateway"
)

// WebToolsSetting is managed by config.GlobalConfig.Register. It holds no
// credentials: gateway calls use the keys of the channels that serve the
// deepseek-search and deepseek-fetch models.
type WebToolsSetting struct {
	Enabled bool `json:"enabled"`
	// DefaultMode applies to channels that do not set their own
	// web_tools_mode.
	DefaultMode string `json:"default_mode"`
	// NativeAffinityRules names channel affinity rules (such as the Claude Code
	// and Codex CLI traces) whose requests keep the upstream's own tools on
	// channels that do not set their own mode.
	NativeAffinityRules []string `json:"native_affinity_rules"`
	// ToolGroup is the group whose deepseek-search and deepseek-fetch channels
	// run gateway calls. Empty uses the group serving the request.
	ToolGroup          string `json:"tool_group"`
	MaxCallsPerRequest int    `json:"max_calls_per_request"`
	SearchResultCount  int    `json:"search_result_count"`
	FetchMaxChars      int    `json:"fetch_max_chars"`
}

var webToolsSetting = WebToolsSetting{
	DefaultMode:         WebToolsModeNative,
	NativeAffinityRules: []string{"codex cli trace", "claude cli trace"},
	MaxCallsPerRequest:  8,
	SearchResultCount:   5,
	FetchMaxChars:       40000,
}

func init() {
	config.GlobalConfig.Register("web_tools_setting", &webToolsSetting)
}

func GetWebToolsSetting() *WebToolsSetting {
	return &webToolsSetting
}

// CallLimit bounds the gateway tool calls of one request.
func (s *WebToolsSetting) CallLimit() int {
	if s.MaxCallsPerRequest <= 0 {
		return 8
	}
	return min(s.MaxCallsPerRequest, 32)
}

// ResultCount is the number of results a gateway search requests.
func (s *WebToolsSetting) ResultCount() int {
	if s.SearchResultCount <= 0 {
		return 5
	}
	return min(s.SearchResultCount, 20)
}

// FetchCharLimit bounds the page text a gateway fetch passes to the model.
func (s *WebToolsSetting) FetchCharLimit() int {
	if s.FetchMaxChars <= 0 {
		return 40000
	}
	return min(max(s.FetchMaxChars, 1000), 200000)
}
