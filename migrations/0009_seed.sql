-- 0009_seed.sql
-- Seed lookup tables. Tokens/pairs are intentionally narrow (mvrk + usdt) so the live
-- job has something to collect on a fresh DB; expand by adding rows here or via ops
-- tooling (admin SQL).
--
-- DO NOTHING (not DO UPDATE): this file re-runs on every deploy (no migration
-- tracking table), so DO UPDATE would clobber operator state each time — e.g.
-- `UPDATE tokens SET enabled=false` to pause a token during a CoinGecko incident,
-- or a decimals/cg_id correction, would be silently reverted on the next deploy
-- and the live job would resume. Seeding is insert-only; ongoing edits are ops SQL.

INSERT INTO sources (code, name, kind) VALUES
    ('coingecko', 'CoinGecko',        'cex'),
    ('equiteez',  'Equiteez Indexer', 'indexer')
ON CONFLICT (code) DO NOTHING;

INSERT INTO tokens (symbol, name, decimals, cg_id, enabled) VALUES
    ('mvrk', 'Mavryk Network', 6, 'mavryk-network', TRUE),
    ('usdt', 'Tether',         6, 'tether',         TRUE)
ON CONFLICT (symbol) DO NOTHING;
