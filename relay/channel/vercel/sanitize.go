package vercel

import (
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// Gemini upstream detection and request sanitization for Vercel AI Gateway.
//
// Probe evidence (2026-09, Vercel → google/gemini-3.8-flash, /v1/messages and
// /v1/chat/completions):
//
//	H7: thinking text without signature + tool_use history → 200
//	H8/H9/H10/G4: thinking with signature + tool_use same assistant message → 400
//	  (empty error body — Vercel copies the signature onto
//	  functionCall.thoughtSignature and Gemini rejects it)
//	E/E2: system/developer after user/assistant on the OAI path → 400
//	A/G1/G2: Claude-format top-level system → 200 (no change needed)
//
// Hence: keep thinking text, delete only the signature; hoist system/developer
// to the front on the OAI path. Both transforms run only for Google upstream
// models so anthropic/* replay keeps valid Anthropic thinking signatures.

// geminiUpstream reports whether the upstream model is served through Vercel's
// @ai-sdk/google converter — the only converter that rejects mid-array system
// and thinking signatures. Prefers the request model (already mapped by
// ModelMappedHelper) and falls back to the relay info.
func geminiUpstream(model string, info *relaycommon.RelayInfo) bool {
	name := model
	if name == "" && info != nil {
		name = info.UpstreamModelName
	}
	m := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(m, "google/") || strings.Contains(m, "gemini")
}

// sanitizeClaudeThinkingSignatures strips Gemini-hostile thinking signatures
// from one message's content blocks while keeping the thinking text (H7). The
// content is the wire-decoded shape: a plain string, or []any of map[string]any
// blocks. Unstructured values are returned unchanged.
func sanitizeClaudeThinkingSignatures(content any) any {
	blocks, ok := content.([]any)
	if !ok {
		return content
	}
	kept := make([]any, 0, len(blocks))
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			kept = append(kept, raw)
			continue
		}
		blockType, _ := block["type"].(string)
		switch blockType {
		case "thinking":
			// Keep the reasoning text; only the signature is poisonous (H8-H10).
			delete(block, "signature")
			kept = append(kept, block)
		case "redacted_thinking":
			// Opaque Anthropic-only blob with no meaning for Gemini; dropped
			// defensively since its data field is unverified against Vercel's
			// converter.
		default:
			kept = append(kept, block)
		}
	}
	return kept
}

// hoistOpenAISystemMessages moves system/developer messages to the front of the
// conversation with their relative order preserved (stable partition). Vercel's
// OAI→Google converter throws when any system message follows a user/assistant
// message (E/E2 — SillyTavern mid-array system).
func hoistOpenAISystemMessages(messages []dto.Message) []dto.Message {
	prefix := make([]dto.Message, 0, 2)
	rest := make([]dto.Message, 0, len(messages))
	for _, m := range messages {
		if m.Role == "system" || m.Role == "developer" {
			prefix = append(prefix, m)
		} else {
			rest = append(rest, m)
		}
	}
	if len(prefix) == 0 {
		return messages
	}
	return append(prefix, rest...)
}
