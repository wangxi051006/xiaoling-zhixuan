-- 催单：独立业务记录，不改变订单金额/库存/支付/履约状态。
-- 同一订单的并发提交由订单行锁 + 唯一 requestId 保证；冷却和次数上限在事务内校验。
CREATE TABLE IF NOT EXISTS urge_policies (
  store_id BIGINT NOT NULL PRIMARY KEY,
  cooldown_seconds INT NOT NULL DEFAULT 300,
  max_per_order INT NOT NULL DEFAULT 3,
  enabled TINYINT NOT NULL DEFAULT 1,
  version BIGINT NOT NULL DEFAULT 1,
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_urge_policy_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  CONSTRAINT ck_urge_policy CHECK (cooldown_seconds >= 0 AND max_per_order >= 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS order_urges (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  order_id BIGINT NOT NULL,
  store_id BIGINT NOT NULL,
  requester_id BIGINT NOT NULL,
  request_id VARCHAR(128) NOT NULL,
  request_hash CHAR(64) NOT NULL,
  reason VARCHAR(200) NOT NULL DEFAULT '',
  status ENUM('RECEIVED','HANDLED') NOT NULL DEFAULT 'RECEIVED',
  notification_status ENUM('PENDING','SENT','FAILED','CANCELLED') NOT NULL DEFAULT 'PENDING',
  notification_target ENUM('STORE','STORE_AND_COURIER') NOT NULL DEFAULT 'STORE',
  urge_number INT NOT NULL,
  version BIGINT NOT NULL DEFAULT 1,
  handled_by BIGINT NULL,
  handled_at DATETIME(6) NULL,
  handle_note VARCHAR(255) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_urge_order FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT,
  CONSTRAINT fk_urge_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  CONSTRAINT fk_urge_requester FOREIGN KEY (requester_id) REFERENCES users(id) ON DELETE RESTRICT,
  CONSTRAINT fk_urge_handler FOREIGN KEY (handled_by) REFERENCES users(id) ON DELETE RESTRICT,
  CONSTRAINT uq_urge_request UNIQUE (requester_id, request_id),
  CONSTRAINT ck_urge_number CHECK (urge_number >= 1),
  KEY idx_urge_order (order_id, created_at, id),
  KEY idx_urge_store_status (store_id, status, created_at, id),
  KEY idx_urge_notification (notification_status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- 数据库支持的站内提醒。recipient_type=STORE 时 recipient_id 是 store_id；
-- recipient_type=USER 时 recipient_id 是员工/配送员 user_id。
CREATE TABLE IF NOT EXISTS notifications (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  recipient_type ENUM('STORE','USER') NOT NULL,
  recipient_id BIGINT NOT NULL,
  store_id BIGINT NOT NULL,
  order_id BIGINT NOT NULL,
  urge_id BIGINT NOT NULL,
  title VARCHAR(120) NOT NULL,
  body VARCHAR(500) NOT NULL DEFAULT '',
  read_at DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_notification_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  CONSTRAINT fk_notification_order FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT,
  CONSTRAINT fk_notification_urge FOREIGN KEY (urge_id) REFERENCES order_urges(id) ON DELETE RESTRICT,
  CONSTRAINT uq_notification_recipient UNIQUE (urge_id, recipient_type, recipient_id),
  KEY idx_notification_store (recipient_type, recipient_id, created_at, id),
  KEY idx_notification_read (recipient_type, recipient_id, read_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
