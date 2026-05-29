package tickers

import "testing"

func TestNewGroupBy(t *testing.T) {
	cases := []struct {
		in    string
		want  GroupBy
		isErr bool
	}{
		{"exchange", GroupByExchange, false},
		{"target", GroupByTarget, false},
		{"EXCHANGE", GroupByExchange, false},
		{"  target  ", GroupByTarget, false},
		{"", "", true},
		{"foo", "", true},
		{"by_exchange", "", true},
	}
	for _, c := range cases {
		got, err := NewGroupBy(c.in)
		if (err != nil) != c.isErr {
			t.Errorf("NewGroupBy(%q): err=%v want isErr=%v", c.in, err, c.isErr)
			continue
		}
		if got != c.want {
			t.Errorf("NewGroupBy(%q): got %q want %q", c.in, got, c.want)
		}
	}
}

func TestClassifyExchangeKind(t *testing.T) {
	cases := []struct {
		in   string
		want ExchangeKind
	}{
		{"binance", ExchangeKindCEX},
		{"kraken", ExchangeKindCEX},
		{"uniswap_v3", ExchangeKindDEX},
		{"UNISWAP_V3", ExchangeKindDEX},
		{"  raydium  ", ExchangeKindDEX},
		{"", ExchangeKindCEX},
		{"some-future-exchange", ExchangeKindCEX},
		{"quipuswap", ExchangeKindDEX},
	}
	for _, c := range cases {
		got := ClassifyExchangeKind(c.in)
		if got != c.want {
			t.Errorf("ClassifyExchangeKind(%q): got %q want %q", c.in, got, c.want)
		}
	}
}
