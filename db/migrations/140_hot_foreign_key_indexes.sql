-- Hot child-table foreign keys had no index with the referencing column first,
-- so parent deletes/updates and the legacy importer's per-invoice lookups forced
-- sequential scans as real data grows. None of these columns is covered by an
-- existing index prefix:
--   invoice_items: (id) primary key, unique (legacy_source,legacy_id), partial
--                  (service_id,invoice_id) - no invoice_id prefix.
--   order_items:   (id) primary key, unique (legacy_source,legacy_id), gin
--                  (config_options) - no order_id prefix.
--   services:      (id) primary key, unique (order_item_id,unit_number), and
--                  indexes on order_id/status/user_id/domain plus partial
--                  resource indexes - no product_id prefix.
-- IF NOT EXISTS keeps the migration re-runnable on databases where an operator
-- already created an equivalent index.
CREATE INDEX IF NOT EXISTS invoice_items_invoice ON invoice_items(invoice_id);
CREATE INDEX IF NOT EXISTS order_items_order ON order_items(order_id);
CREATE INDEX IF NOT EXISTS services_product ON services(product_id);
