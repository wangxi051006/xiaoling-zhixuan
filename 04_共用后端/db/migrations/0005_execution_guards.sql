-- Creating these independent tables is safe to resume after an interrupted migration.
-- Serialize urge keys across different orders without locking absent index gaps.
CREATE TABLE IF NOT EXISTS urge_request_locks (
  requester_id BIGINT NOT NULL,
  request_id VARCHAR(128) NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (requester_id, request_id),
  CONSTRAINT fk_urge_lock_user FOREIGN KEY (requester_id) REFERENCES users(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- A regenerated code has a new id, and therefore a fresh bounded attempt budget.
CREATE TABLE IF NOT EXISTS delivery_code_attempts (
  code_id BIGINT NOT NULL PRIMARY KEY,
  failed_attempts INT NOT NULL DEFAULT 0,
  CONSTRAINT fk_delivery_attempt_code FOREIGN KEY (code_id) REFERENCES delivery_codes(id) ON DELETE RESTRICT,
  CONSTRAINT ck_delivery_attempt_count CHECK (failed_attempts >= 0 AND failed_attempts <= 5)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
