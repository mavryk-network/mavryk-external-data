-- 0018_rwa_launches_quote_addr.sql
-- On-chain address of the launch's quote (payment) token.
--
-- Why: /v1/pairs/rwa serves quote_addr for orderbook pairs (0017) but had to
-- return null for primary-issuance assets — the launchpad has no orderbook
-- currency row. The address does exist upstream, on the sale-option payment
-- rows (`launchpad_sale_option_payment.token`), and the sync already reads
-- those rows to derive the base-tier price and the quote SYMBOL; it just
-- discarded the address next to them.
--
-- Migration semantics: existing rows get quote_addr = NULL; the next discovery
-- tick (hourly) fills it in. Nullable by design — a payment row without a
-- nested token ref must not block the upsert.

ALTER TABLE rwa_launches ADD COLUMN IF NOT EXISTS quote_addr TEXT;

COMMENT ON COLUMN rwa_launches.quote_addr IS
    'On-chain contract of the quote (payment) token, taken from the same sale-option payment that sets the base-tier price. NULL until the first sync after this migration.';
