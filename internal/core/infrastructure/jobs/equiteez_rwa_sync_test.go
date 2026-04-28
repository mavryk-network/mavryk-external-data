package jobs

import (
	"encoding/json"
	"testing"

	"quotes/internal/core/infrastructure/interactions/equiteez"
)

func tok(addr string, id int, tokenMeta, parentMeta string) equiteez.TokenWithOrderbooks {
	t := equiteez.TokenWithOrderbooks{Address: addr, TokenID: id}
	if tokenMeta != "" {
		t.TokenMetadata = json.RawMessage(tokenMeta)
	}
	if parentMeta != "" {
		t.Metadata = json.RawMessage(parentMeta)
	}
	return t
}

func TestDeriveBaseSymbol(t *testing.T) {
	cases := []struct {
		name string
		in   equiteez.TokenWithOrderbooks
		want string
	}{
		{
			name: "token_metadata_symbol_wins",
			in:   tok("KT1M3U8zRf3AwaerkTXCThcffUqWpie3UMoj", 0, `{"symbol":"USDT","name":"Tether","decimals":6}`, ""),
			want: "USDT",
		},
		{
			name: "metadata_symbol_when_token_metadata_missing",
			in:   tok("KT1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 0, "", `{"symbol":"MVRK"}`),
			want: "MVRK",
		},
		{
			name: "name_fallback_when_symbol_absent",
			in:   tok("KT1abcdef0000000000000000000000000000z", 0, `{"name":"Mavryk Network"}`, ""),
			want: "Mavryk Network",
		},
		{
			name: "token_metadata_takes_precedence_over_metadata",
			in:   tok("KT1zzz", 0, `{"symbol":"NEW"}`, `{"symbol":"OLD"}`),
			want: "NEW",
		},
		{
			name: "fa2_token_id_when_no_metadata",
			in:   tok("KT1abcdefghijklmnopqrstuvwxyz0123456789", 7, "", ""),
			want: "equiteez:7",
		},
		{
			name: "shortened_address_for_fa12_no_metadata",
			in:   tok("KT1M3U8zRf3AwaerkTXCThcffUqWpie3UMoj", 0, "", ""),
			want: "KT1M3…UMoj",
		},
		{
			name: "empty_symbol_string_falls_through_to_name",
			in:   tok("KT1abcdefghijklmnopqrstuvwxyz0123456789", 0, `{"symbol":"  ","name":"Fallback"}`, ""),
			want: "Fallback",
		},
		{
			name: "invalid_json_in_metadata_does_not_panic",
			in:   tok("KT1abcdefghijklmnopqrstuvwxyz0123456789", 3, `{not json`, ""),
			want: "equiteez:3",
		},
		{
			name: "non_string_symbol_field_skipped",
			in:   tok("KT1abcdefghijklmnopqrstuvwxyz0123456789", 0, `{"symbol":42,"name":"Numeric"}`, ""),
			want: "Numeric",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveBaseSymbol(c.in); got != c.want {
				t.Errorf("deriveBaseSymbol = %q, want %q", got, c.want)
			}
		})
	}
}

func TestShortAddr(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"short", "short"},
		{"KT1M3U8zRf3AwaerkTXCThcffUqWpie3UMoj", "KT1M3…UMoj"},
		{"abcdefghij", "abcdefghij"}, // 10 chars: too short to truncate (head+tail+1=10)
		{"abcdefghijk", "abcde…hijk"},
	}
	for _, c := range cases {
		if got := shortAddr(c.in); got != c.want {
			t.Errorf("shortAddr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMetadataField(t *testing.T) {
	cases := []struct {
		raw, key, want string
	}{
		{`{"symbol":"USDT"}`, "symbol", "USDT"},
		{`{"symbol":"  trim me  "}`, "symbol", "trim me"},
		{`{"name":"Tether"}`, "symbol", ""},
		{``, "symbol", ""},
		{`null`, "symbol", ""},
		{`"just a string"`, "symbol", ""},
		{`[]`, "symbol", ""},
		{`{not json`, "symbol", ""},
		{`{"symbol":42}`, "symbol", ""},
	}
	for _, c := range cases {
		var raw []byte
		if c.raw != "" {
			raw = []byte(c.raw)
		}
		if got := metadataField(raw, c.key); got != c.want {
			t.Errorf("metadataField(%q, %q) = %q, want %q", c.raw, c.key, got, c.want)
		}
	}
}
