-- 0009_seed.sql
-- Seed lookup tables. Tokens/pairs are intentionally narrow (mvrk + usdt) so the live
-- job has something to collect on a fresh DB; expand by adding rows here or via ops
-- tooling (admin SQL).

INSERT INTO sources (code, name, kind) VALUES
    ('coingecko', 'CoinGecko',        'cex'),
    ('equiteez',  'Equiteez Indexer', 'indexer')
ON CONFLICT (code) DO UPDATE
    SET name = EXCLUDED.name, kind = EXCLUDED.kind;

INSERT INTO tokens (symbol, name, decimals, cg_id, enabled) VALUES
    ('mvrk', 'Mavryk Network', 6, 'mavryk-network', TRUE),
    ('usdt', 'Tether',         6, 'tether',         TRUE)
ON CONFLICT (symbol) DO UPDATE
    SET name = EXCLUDED.name,
        decimals = EXCLUDED.decimals,
        cg_id = EXCLUDED.cg_id,
        enabled = EXCLUDED.enabled;
