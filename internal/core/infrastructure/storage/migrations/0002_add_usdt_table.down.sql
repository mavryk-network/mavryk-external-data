-- +migrate Down
-- Drop usdt table
DROP TABLE IF EXISTS mev.usdt CASCADE;
