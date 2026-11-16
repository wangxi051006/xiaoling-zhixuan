-- 0007: per-SKU low stock threshold.
-- Restart-safe: MySQL has no "ADD COLUMN IF NOT EXISTS", so the DDL only runs when
-- information_schema says the column is missing.
SET @ddl = (SELECT IF((SELECT COUNT(*) FROM information_schema.columns
    WHERE table_schema=DATABASE() AND table_name='skus' AND column_name='low_stock_threshold')=0,
  'ALTER TABLE skus ADD COLUMN low_stock_threshold INT NOT NULL DEFAULT 5 AFTER active',
  'DO 0'));
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;

SET @ddl = (SELECT IF((SELECT COUNT(*) FROM information_schema.table_constraints
    WHERE table_schema=DATABASE() AND table_name='skus' AND constraint_name='ck_sku_low_stock_threshold')=0,
  'ALTER TABLE skus ADD CONSTRAINT ck_sku_low_stock_threshold CHECK (low_stock_threshold >= 0)',
  'DO 0'));
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;
