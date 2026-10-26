package equiteez

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPricingIdentityDistinguishesMissingTokenIDFromZero(t *testing.T) {
	for _, raw := range []string{`{}`, `{"token_id":null}`, `{"token_id":0}`, `{"token_id":1}`} {
		known := raw == `{"token_id":0}` || raw == `{"token_id":1}`
		var base TokenWithOrderbooks
		var quote TokenQuoteToken
		var launch LaunchTokenRef
		require.NoError(t, json.Unmarshal([]byte(raw), &base))
		require.NoError(t, json.Unmarshal([]byte(raw), &quote))
		require.NoError(t, json.Unmarshal([]byte(raw), &launch))
		require.Equal(t, known, base.TokenIDKnown)
		require.Equal(t, known, quote.TokenIDKnown)
		require.Equal(t, known, launch.TokenIDKnown)
	}
}

func TestPrimaryPricingRequiresOneVerifiedPaymentIdentity(t *testing.T) {
	var row LaunchRow
	require.NoError(t, json.Unmarshal([]byte(`{"token":{"address":"asset","token_id":0},"sale_options":[{"payments":[{"name":"usdt","price":"100000000","token":{"address":"quote","token_id":0}}]}]}`), &row))
	require.NotNil(t, row.PricingQuoteTokenID("usdt", "quote"))
	row.SaleOptions[0].Payments = append(row.SaleOptions[0].Payments, LaunchPaymentRow{Name: "eur", Token: &LaunchTokenRef{Address: "other", TokenIDKnown: true}})
	require.Nil(t, row.PricingQuoteTokenID("usdt", "quote"))
	row.SaleOptions[0].Payments = row.SaleOptions[0].Payments[:1]
	row.SaleOptions[0].Payments[0].Token.TokenIDKnown = false
	require.Nil(t, row.PricingQuoteTokenID("usdt", "quote"))
	row.SaleOptions[0].Payments[0].Token.TokenIDKnown = true
	row.Token.TokenID = 1
	require.Nil(t, row.PricingQuoteTokenID("usdt", "quote"))
}
