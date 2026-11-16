-- 0006: delivery exception recovery.
-- Restart-safe: every DDL is guarded by an information_schema check, because MySQL
-- has no "ADD COLUMN IF NOT EXISTS" and a crash mid-file would otherwise make the
-- next run fail on an already-added column.
SET @ddl = (SELECT IF((SELECT COUNT(*) FROM information_schema.columns
    WHERE table_schema=DATABASE() AND table_name='delivery_tasks' AND column_name='exception_reason')=0,
  'ALTER TABLE delivery_tasks ADD COLUMN exception_reason VARCHAR(40) NULL AFTER assignment_generation',
  'DO 0'));
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;

SET @ddl = (SELECT IF((SELECT COUNT(*) FROM information_schema.columns
    WHERE table_schema=DATABASE() AND table_name='delivery_tasks' AND column_name='exception_note')=0,
  'ALTER TABLE delivery_tasks ADD COLUMN exception_note VARCHAR(255) NOT NULL DEFAULT '''' AFTER exception_reason',
  'DO 0'));
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;

SET @ddl = (SELECT IF((SELECT COUNT(*) FROM information_schema.columns
    WHERE table_schema=DATABASE() AND table_name='delivery_tasks' AND column_name='exception_at')=0,
  'ALTER TABLE delivery_tasks ADD COLUMN exception_at DATETIME(6) NULL AFTER exception_note',
  'DO 0'));
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;

SET @ddl = (SELECT IF((SELECT COUNT(*) FROM information_schema.columns
    WHERE table_schema=DATABASE() AND table_name='delivery_tasks' AND column_name='exception_resolved_at')=0,
  'ALTER TABLE delivery_tasks ADD COLUMN exception_resolved_at DATETIME(6) NULL AFTER exception_at',
  'DO 0'));
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;

SET @ddl = (SELECT IF((SELECT COUNT(*) FROM information_schema.columns
    WHERE table_schema=DATABASE() AND table_name='delivery_tasks' AND column_name='exception_resolved_by')=0,
  'ALTER TABLE delivery_tasks ADD COLUMN exception_resolved_by BIGINT NULL AFTER exception_resolved_at',
  'DO 0'));
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;

SET @ddl = (SELECT IF((SELECT COUNT(*) FROM information_schema.statistics
    WHERE table_schema=DATABASE() AND table_name='delivery_tasks' AND index_name='idx_task_exception')=0,
  'CREATE INDEX idx_task_exception ON delivery_tasks (store_id, status, exception_at)',
  'DO 0'));
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;

-- A regenerated receipt code retires its predecessor. Keeping "retired" separate
-- from "consumed at signing" preserves the single-consumption audit trail.
SET @ddl = (SELECT IF((SELECT COUNT(*) FROM information_schema.columns
    WHERE table_schema=DATABASE() AND table_name='delivery_codes' AND column_name='superseded_at')=0,
  'ALTER TABLE delivery_codes ADD COLUMN superseded_at DATETIME(6) NULL AFTER used_at',
  'DO 0'));
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;
