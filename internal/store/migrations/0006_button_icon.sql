-- A button may name its own icon: any service id from the live AdGuard
-- catalogue (locked icon_source), checked by the API, not here. NULL means no
-- choice — Home falls back to the first service's icon as before — so every
-- existing row keeps today's look without a backfill.
ALTER TABLE buttons ADD COLUMN icon TEXT;
