package dto

import (
	"fmt"
	"slices"
	"strings"
)

// Native upstream protocols of a model-routed channel (OpenCode Zen/Go,
// Command Code). Such a gateway serves each model on one or more of them
// behind a single API key.
const (
	ModelProtocolChatCompletions = "chat_completions"
	ModelProtocolResponses       = "responses"
	ModelProtocolAnthropic       = "anthropic"
)

const (
	maxModelProtocolRules             = 64
	maxModelProtocolRulePatternLength = 128
)

// ModelProtocolRule maps a model pattern to the native upstream protocols that
// serve the model. Pattern is an exact model ID or a prefix followed by a single
// trailing "*"; matching ignores case. Protocol is the preferred protocol and
// Protocols optionally lists every protocol the model supports: a request whose
// inbound protocol is listed is forwarded unchanged, any other request is
// converted to Protocol.
type ModelProtocolRule struct {
	Pattern   string   `json:"pattern"`
	Protocol  string   `json:"protocol,omitempty"`
	Protocols []string `json:"protocols,omitempty"`
}

func IsModelProtocol(protocol string) bool {
	switch protocol {
	case ModelProtocolChatCompletions, ModelProtocolResponses, ModelProtocolAnthropic:
		return true
	default:
		return false
	}
}

// ProtocolSet returns the protocols of the rule with the preferred protocol
// first, skipping unknown and duplicate entries. An empty Protocol falls back to
// the first listed protocol; a rule without any known protocol yields nil.
func (r ModelProtocolRule) ProtocolSet() []string {
	preferred := strings.TrimSpace(r.Protocol)
	if preferred == "" && len(r.Protocols) > 0 {
		preferred = strings.TrimSpace(r.Protocols[0])
	}
	if !IsModelProtocol(preferred) {
		return nil
	}
	set := []string{preferred}
	for _, protocol := range r.Protocols {
		protocol = strings.TrimSpace(protocol)
		if IsModelProtocol(protocol) && !slices.Contains(set, protocol) {
			set = append(set, protocol)
		}
	}
	return set
}

// Matches reports whether the rule pattern matches the model ID.
func (r ModelProtocolRule) Matches(model string) bool {
	pattern := strings.ToLower(strings.TrimSpace(r.Pattern))
	model = strings.ToLower(strings.TrimSpace(model))
	if pattern == "" || model == "" {
		return false
	}
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(model, prefix)
	}
	return model == pattern
}

// MatchModelProtocolRules returns the protocol set of the first rule that
// matches model. Rules without a known protocol are skipped.
func MatchModelProtocolRules(rules []ModelProtocolRule, model string) ([]string, bool) {
	for _, rule := range rules {
		set := rule.ProtocolSet()
		if len(set) > 0 && rule.Matches(model) {
			return set, true
		}
	}
	return nil, false
}

// ValidateModelProtocolRules validates the channel-level model protocol rules
// of a model-routed channel. An empty list keeps the built-in rules.
func (s *ChannelOtherSettings) ValidateModelProtocolRules() error {
	if s == nil {
		return nil
	}
	if len(s.ModelProtocolRules) > maxModelProtocolRules {
		return fmt.Errorf("model_protocol_rules supports at most %d entries", maxModelProtocolRules)
	}
	for i, rule := range s.ModelProtocolRules {
		pattern := strings.TrimSpace(rule.Pattern)
		switch {
		case pattern == "":
			return fmt.Errorf("model_protocol_rules[%d].pattern is required", i)
		case len(pattern) > maxModelProtocolRulePatternLength:
			return fmt.Errorf("model_protocol_rules[%d].pattern is longer than %d characters", i, maxModelProtocolRulePatternLength)
		case strings.ContainsAny(pattern, " \t"):
			return fmt.Errorf("model_protocol_rules[%d].pattern must not contain whitespace", i)
		}
		if star := strings.Count(pattern, "*"); star > 1 || star == 1 && !strings.HasSuffix(pattern, "*") {
			return fmt.Errorf("model_protocol_rules[%d].pattern may only end with a single * wildcard", i)
		}
		protocol := strings.TrimSpace(rule.Protocol)
		if protocol != "" && !IsModelProtocol(protocol) {
			return fmt.Errorf("model_protocol_rules[%d].protocol must be chat_completions, responses, or anthropic", i)
		}
		for _, listed := range rule.Protocols {
			if !IsModelProtocol(strings.TrimSpace(listed)) {
				return fmt.Errorf("model_protocol_rules[%d].protocols must only contain chat_completions, responses, or anthropic", i)
			}
		}
		if protocol == "" && len(rule.Protocols) == 0 {
			return fmt.Errorf("model_protocol_rules[%d].protocol is required", i)
		}
	}
	return nil
}
