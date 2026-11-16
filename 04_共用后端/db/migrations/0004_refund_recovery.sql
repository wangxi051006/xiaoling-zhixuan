-- DDL additions and historical reconciliation are orchestrated by internal/db.
-- Every ALTER is checked individually for safe restart. Recovery is transactional:
-- valid orders get quantities reconstructed from successful/unresolved refunds;
-- inconsistent orders keep their evidence and are blocked from further mutations.
CREATE TABLE IF NOT EXISTS refund_recovery_blocks (
  order_id BIGINT NOT NULL PRIMARY KEY,
  reason VARCHAR(128) NOT NULL,
  detected_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_refund_recovery_order FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
