-- 0008: batch order operations (merchant). Restart-safe.
-- Targets store the exact order/version the user confirmed, so a replay can tell
-- "already handled" from "version moved on". Items deliberately have NO foreign key
-- to orders: a nonexistent or cross-store order must still be recordable as a stable
-- failure result without fabricating rows or disabling FK checks.
CREATE TABLE IF NOT EXISTS batch_operations (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  store_id BIGINT NOT NULL,
  actor_id BIGINT NOT NULL,
  kind VARCHAR(24) NOT NULL,
  request_id VARCHAR(128) NOT NULL,
  payload_hash CHAR(64) NOT NULL,
  expected_count INT NOT NULL DEFAULT 0,
  status ENUM('RUNNING','DONE') NOT NULL DEFAULT 'RUNNING',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT uq_batch_actor_kind_request UNIQUE (actor_id, kind, request_id),
  CONSTRAINT ck_batch_expected_count CHECK (expected_count >= 0),
  KEY idx_batch_store_created (store_id, created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS batch_operation_targets (
  batch_id BIGINT NOT NULL,
  order_id BIGINT NOT NULL,
  expected_version BIGINT NOT NULL,
  PRIMARY KEY (batch_id, order_id),
  CONSTRAINT fk_batch_target_batch FOREIGN KEY (batch_id) REFERENCES batch_operations(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS batch_operation_items (
  batch_id BIGINT NOT NULL,
  order_id BIGINT NOT NULL,
  result ENUM('SUCCESS','FAILED','SKIPPED') NOT NULL,
  error_code VARCHAR(40) NOT NULL DEFAULT '',
  message VARCHAR(200) NOT NULL DEFAULT '',
  version_after BIGINT NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (batch_id, order_id),
  CONSTRAINT fk_batch_item_batch FOREIGN KEY (batch_id) REFERENCES batch_operations(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
