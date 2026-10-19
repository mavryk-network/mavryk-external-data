package jobs

import (
	"encoding/json"
	"fmt"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"strings"
)

// deriveBaseSymbol extracts a human label for the base asset.
//
// Resolution order (first non-empty wins):
//  1. `token_metadata.symbol`  — TZIP-21/12 canonical field.
//  2. `metadata.symbol`        — some indexers expose the same data on the parent.
//  3. `token_metadata.name`    — fallback when symbol is omitted.
//  4. `metadata.name`.
//  5. `equiteez:<token_id>`    — for FA2 tokens with explicit ids.
//  6. shortened token contract — never returns the full KT1… in `base_symbol`,
//     so the column always carries something distinguishable in the UI.
//
// NOTE: a manual `UPDATE rwa_pairs SET base_symbol=...` does NOT survive the
// sync — UpsertRWAPair re-derives base/quote symbols on every pass.
func deriveBaseSymbol(tok equiteez.TokenWithOrderbooks) string {
	for _, raw := range [][]byte{tok.TokenMetadata, tok.Metadata} {
		if s := metadataField(raw, "symbol"); s != "" {
			return s
		}
	}
	for _, raw := range [][]byte{tok.TokenMetadata, tok.Metadata} {
		if s := metadataField(raw, "name"); s != "" {
			return s
		}
	}
	if tok.TokenID > 0 {
		return fmt.Sprintf("equiteez:%d", tok.TokenID)
	}
	return shortAddr(tok.Address)
}

// metadataField extracts a string field from a JSON blob. Tolerates null /
// invalid / non-object payloads (returns ""). Common keys: "symbol", "name".
func metadataField(raw []byte, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// shortAddr renders a Tezos address as KT1M3…UMoj (5+1+4 chars). Used as a
// last-resort base_symbol so the column never carries the full 36-char KT1…
// — an operator looking at the table sees something distinguishable.
func shortAddr(addr string) string {
	const head, tail = 5, 4
	if len(addr) <= head+tail+1 {
		return addr
	}
	return addr[:head] + "…" + addr[len(addr)-tail:]
}
