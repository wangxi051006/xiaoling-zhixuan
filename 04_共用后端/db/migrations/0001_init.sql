-- 校零智选 MySQL 8.0+ schema. InnoDB, utf8mb4, integer cents only.
-- IDs are BIGINT and exposed as strings by the API.
SET NAMES utf8mb4 COLLATE utf8mb4_bin;

CREATE TABLE IF NOT EXISTS stores (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  name VARCHAR(80) NOT NULL,
  active TINYINT NOT NULL DEFAULT 1,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS buildings (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  store_id BIGINT NOT NULL,
  name VARCHAR(80) NOT NULL,
  delivery_fee_cents BIGINT NOT NULL DEFAULT 0,
  active TINYINT NOT NULL DEFAULT 1,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_buildings_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  CONSTRAINT uq_buildings_store_name UNIQUE (store_id, name),
  CONSTRAINT ck_buildings_fee CHECK (delivery_fee_cents >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS users (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  phone VARCHAR(20) NULL,
  nickname VARCHAR(80) NOT NULL DEFAULT '',
  password_hash VARCHAR(255) NULL,
  status ENUM('ACTIVE','DISABLED') NOT NULL DEFAULT 'ACTIVE',
  session_version BIGINT NOT NULL DEFAULT 1,
  default_address_id BIGINT NULL,
  is_demo TINYINT NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT uq_users_phone UNIQUE (phone),
  CONSTRAINT ck_users_session_version CHECK (session_version >= 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS wechat_identities (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  user_id BIGINT NOT NULL,
  appid VARCHAR(64) NOT NULL,
  openid VARCHAR(128) NOT NULL,
  unionid VARCHAR(128) NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_wechat_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
  CONSTRAINT uq_wechat_app_openid UNIQUE (appid, openid),
  KEY idx_wechat_user (user_id, appid)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS staff_memberships (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  user_id BIGINT NOT NULL,
  store_id BIGINT NOT NULL,
  active TINYINT NOT NULL DEFAULT 1,
  version BIGINT NOT NULL DEFAULT 1,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_staff_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
  CONSTRAINT fk_staff_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  CONSTRAINT uq_staff_user_store UNIQUE (user_id, store_id),
  KEY idx_staff_store (store_id, active)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS staff_roles (
  membership_id BIGINT NOT NULL,
  role VARCHAR(32) NOT NULL,
  PRIMARY KEY (membership_id, role),
  CONSTRAINT fk_staff_role_membership FOREIGN KEY (membership_id) REFERENCES staff_memberships(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS addresses (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  owner_id BIGINT NOT NULL,
  store_id BIGINT NOT NULL,
  building_id BIGINT NOT NULL,
  building_name_snapshot VARCHAR(80) NOT NULL,
  contact_name VARCHAR(40) NOT NULL,
  phone VARCHAR(20) NOT NULL,
  detail VARCHAR(200) NOT NULL,
  version BIGINT NOT NULL DEFAULT 1,
  deleted_at DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_address_owner FOREIGN KEY (owner_id) REFERENCES users(id) ON DELETE RESTRICT,
  CONSTRAINT fk_address_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  CONSTRAINT fk_address_building FOREIGN KEY (building_id) REFERENCES buildings(id) ON DELETE RESTRICT,
  KEY idx_address_owner (owner_id, id),
  KEY idx_address_building (building_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS categories (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  store_id BIGINT NOT NULL,
  name VARCHAR(60) NOT NULL,
  sort_order INT NOT NULL DEFAULT 0,
  active TINYINT NOT NULL DEFAULT 1,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_categories_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  CONSTRAINT uq_categories_store_name UNIQUE (store_id, name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS products (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  store_id BIGINT NOT NULL,
  category_id BIGINT NOT NULL,
  name VARCHAR(80) NOT NULL,
  description VARCHAR(500) NOT NULL DEFAULT '',
  active TINYINT NOT NULL DEFAULT 1,
  version BIGINT NOT NULL DEFAULT 1,
  is_demo TINYINT NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_products_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  CONSTRAINT fk_products_category FOREIGN KEY (category_id) REFERENCES categories(id) ON DELETE RESTRICT,
  KEY idx_products_store (store_id, active, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS skus (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  store_id BIGINT NOT NULL,
  product_id BIGINT NOT NULL,
  spec_name VARCHAR(80) NOT NULL DEFAULT '标准',
  price_cents BIGINT NOT NULL,
  active TINYINT NOT NULL DEFAULT 1,
  version BIGINT NOT NULL DEFAULT 1,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_skus_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  CONSTRAINT fk_skus_product FOREIGN KEY (product_id) REFERENCES products(id) ON DELETE RESTRICT,
  CONSTRAINT uq_skus_product_spec UNIQUE (product_id, spec_name),
  CONSTRAINT ck_skus_price CHECK (price_cents >= 0),
  KEY idx_skus_store (store_id, active, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS inventory_lots (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  store_id BIGINT NOT NULL,
  sku_id BIGINT NOT NULL,
  batch_no VARCHAR(64) NOT NULL,
  expiry_date DATE NOT NULL,
  qty_on_hand BIGINT NOT NULL DEFAULT 0,
  qty_reserved BIGINT NOT NULL DEFAULT 0,
  unit_cost_cents BIGINT NOT NULL DEFAULT 0,
  version BIGINT NOT NULL DEFAULT 1,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_lots_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  CONSTRAINT fk_lots_sku FOREIGN KEY (sku_id) REFERENCES skus(id) ON DELETE RESTRICT,
  CONSTRAINT uq_lots_batch UNIQUE (store_id, sku_id, batch_no),
  CONSTRAINT ck_lots_qty CHECK (qty_on_hand >= 0 AND qty_reserved >= 0 AND qty_reserved <= qty_on_hand),
  CONSTRAINT ck_lots_cost CHECK (unit_cost_cents >= 0),
  KEY idx_lots_fefo (store_id, sku_id, expiry_date, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS quotes (
  id CHAR(32) NOT NULL PRIMARY KEY,
  user_id BIGINT NOT NULL,
  store_id BIGINT NOT NULL,
  request_hash CHAR(64) NOT NULL,
  payload_json JSON NOT NULL,
  total_cents BIGINT NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_quotes_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
  CONSTRAINT fk_quotes_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  KEY idx_quotes_expiry (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS orders (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  order_no VARCHAR(40) NOT NULL,
  store_id BIGINT NOT NULL,
  buyer_id BIGINT NOT NULL,
  request_id VARCHAR(128) NOT NULL,
  request_hash CHAR(64) NOT NULL,
  status ENUM('PENDING_PAYMENT','PAID','PICKING','READY','DELIVERING','COMPLETED','CANCELLED','CLOSED','PAYMENT_EXCEPTION') NOT NULL DEFAULT 'PENDING_PAYMENT',
  payment_status ENUM('UNPAID','CONFIRMING','PAID','PARTIAL_REFUNDED','REFUNDED','CLOSED') NOT NULL DEFAULT 'UNPAID',
  delivery_status ENUM('UNASSIGNED','ASSIGNED','PICKED_UP','DELIVERING','DELIVERED','EXCEPTION','CANCELLED') NOT NULL DEFAULT 'UNASSIGNED',
  goods_cents BIGINT NOT NULL,
  delivery_cents BIGINT NOT NULL DEFAULT 0,
  discount_cents BIGINT NOT NULL DEFAULT 0,
  total_cents BIGINT NOT NULL,
  currency CHAR(3) NOT NULL DEFAULT 'CNY',
  address_json JSON NOT NULL,
  remark VARCHAR(255) NOT NULL DEFAULT '',
  expires_at DATETIME(6) NOT NULL,
  paid_at DATETIME(6) NULL,
  customer_order_ordinal INT NOT NULL,
  is_first_order TINYINT NOT NULL,
  version BIGINT NOT NULL DEFAULT 1,
  payment_exception TINYINT NOT NULL DEFAULT 0,
  is_demo TINYINT NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_orders_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  CONSTRAINT fk_orders_buyer FOREIGN KEY (buyer_id) REFERENCES users(id) ON DELETE RESTRICT,
  CONSTRAINT uq_orders_no UNIQUE (order_no),
  CONSTRAINT uq_orders_buyer_request UNIQUE (buyer_id, request_id),
  CONSTRAINT ck_orders_amounts CHECK (goods_cents >= 0 AND delivery_cents >= 0 AND discount_cents >= 0 AND total_cents >= 0),
  CONSTRAINT ck_orders_ordinal CHECK (customer_order_ordinal >= 1 AND is_first_order IN (0,1)),
  KEY idx_orders_store_status (store_id, status, created_at, id),
  KEY idx_orders_payment_expiry (payment_status, expires_at, id),
  KEY idx_orders_buyer (buyer_id, created_at, id),
  KEY idx_orders_paid_at (store_id, paid_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS order_items (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  order_id BIGINT NOT NULL,
  sku_id BIGINT NOT NULL,
  product_name VARCHAR(80) NOT NULL,
  spec_name VARCHAR(80) NOT NULL,
  unit_price_cents BIGINT NOT NULL,
  quantity INT NOT NULL,
  goods_cents BIGINT NOT NULL,
  refunded_quantity INT NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_order_items_order FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT,
  CONSTRAINT fk_order_items_sku FOREIGN KEY (sku_id) REFERENCES skus(id) ON DELETE RESTRICT,
  CONSTRAINT ck_order_items_qty CHECK (quantity > 0 AND refunded_quantity >= 0 AND refunded_quantity <= quantity),
  CONSTRAINT ck_order_items_money CHECK (unit_price_cents >= 0 AND goods_cents >= 0),
  KEY idx_order_items_order (order_id, id),
  KEY idx_order_items_sku (sku_id, order_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS customer_order_counters (
  user_id BIGINT NOT NULL PRIMARY KEY,
  total_created BIGINT NOT NULL DEFAULT 0,
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_counter_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
  CONSTRAINT ck_counter_total CHECK (total_created >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS idempotency_keys (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  principal_id BIGINT NOT NULL,
  command_scope VARCHAR(40) NOT NULL,
  request_id VARCHAR(128) NOT NULL,
  request_hash CHAR(64) NOT NULL,
  resource_type VARCHAR(40) NOT NULL DEFAULT '',
  resource_id VARCHAR(64) NOT NULL DEFAULT '',
  response_json JSON NULL,
  status_code INT NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT uq_idempotency_key UNIQUE (principal_id, command_scope, request_id),
  KEY idx_idempotency_resource (resource_type, resource_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS inventory_reservations (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  order_id BIGINT NOT NULL,
  order_item_id BIGINT NOT NULL,
  lot_id BIGINT NOT NULL,
  quantity BIGINT NOT NULL,
  status ENUM('RESERVED','CONSUMED','RELEASED') NOT NULL DEFAULT 'RESERVED',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_res_order FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT,
  CONSTRAINT fk_res_item FOREIGN KEY (order_item_id) REFERENCES order_items(id) ON DELETE RESTRICT,
  CONSTRAINT fk_res_lot FOREIGN KEY (lot_id) REFERENCES inventory_lots(id) ON DELETE RESTRICT,
  CONSTRAINT uq_res_item_lot UNIQUE (order_item_id, lot_id),
  CONSTRAINT ck_res_qty CHECK (quantity > 0),
  KEY idx_res_order (order_id, status),
  KEY idx_res_lot (lot_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS payment_attempts (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  order_id BIGINT NOT NULL,
  mchid VARCHAR(64) NOT NULL,
  appid VARCHAR(64) NOT NULL,
  out_trade_no VARCHAR(64) NOT NULL,
  channel ENUM('WECHAT_JSAPI','WECHAT_H5','WECHAT_MINI','SIMULATED') NOT NULL,
  amount_cents BIGINT NOT NULL,
  currency CHAR(3) NOT NULL DEFAULT 'CNY',
  status ENUM('PREPARING','PREPAY_READY','UNKNOWN','PAID','CLOSED','FAILED') NOT NULL DEFAULT 'PREPARING',
  prepay_id VARCHAR(128) NULL,
  prepay_params JSON NULL,
  prepay_expires_at DATETIME(6) NULL,
  transaction_id VARCHAR(128) NULL,
  paid_at DATETIME(6) NULL,
  last_checked_at DATETIME(6) NULL,
  version BIGINT NOT NULL DEFAULT 1,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_attempt_order FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT,
  CONSTRAINT uq_attempt_order UNIQUE (order_id),
  CONSTRAINT uq_attempt_trade_no UNIQUE (mchid, out_trade_no),
  CONSTRAINT uq_attempt_txn UNIQUE (mchid, transaction_id),
  CONSTRAINT ck_attempt_amount CHECK (amount_cents >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS payment_transactions (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  attempt_id BIGINT NOT NULL,
  order_id BIGINT NOT NULL,
  transaction_id VARCHAR(128) NOT NULL,
  amount_cents BIGINT NOT NULL,
  paid_at DATETIME(6) NOT NULL,
  source ENUM('WECHAT','SIMULATED','MANUAL_VERIFIED') NOT NULL,
  raw_digest CHAR(64) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_txn_attempt FOREIGN KEY (attempt_id) REFERENCES payment_attempts(id) ON DELETE RESTRICT,
  CONSTRAINT fk_txn_order FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT,
  CONSTRAINT uq_txn_id UNIQUE (transaction_id),
  CONSTRAINT uq_txn_attempt UNIQUE (attempt_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS payment_inbox (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  provider VARCHAR(32) NOT NULL,
  event_id VARCHAR(128) NOT NULL,
  event_type VARCHAR(64) NOT NULL,
  payload_digest CHAR(64) NOT NULL,
  payload_json JSON NOT NULL,
  state ENUM('RECEIVED','PROCESSING','PROCESSED','FAILED') NOT NULL DEFAULT 'RECEIVED',
  attempts INT NOT NULL DEFAULT 0,
  next_run_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  order_id BIGINT NULL,
  last_error VARCHAR(500) NOT NULL DEFAULT '',
  received_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  processed_at DATETIME(6) NULL,
  CONSTRAINT uq_inbox_event UNIQUE (provider, event_id),
  KEY idx_inbox_pending (state, next_run_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS refunds (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  order_id BIGINT NOT NULL,
  after_sale_id VARCHAR(128) NOT NULL DEFAULT '',
  out_refund_no VARCHAR(64) NOT NULL,
  amount_cents BIGINT NOT NULL,
  currency CHAR(3) NOT NULL DEFAULT 'CNY',
  status ENUM('REQUESTED','PROCESSING','SUCCESS','FAILED','CLOSED') NOT NULL DEFAULT 'REQUESTED',
  provider_refund_id VARCHAR(128) NULL,
  reason VARCHAR(255) NOT NULL DEFAULT '',
  requested_by BIGINT NOT NULL,
  succeeded_at DATETIME(6) NULL,
  version BIGINT NOT NULL DEFAULT 1,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_refund_order FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT,
  CONSTRAINT fk_refund_requester FOREIGN KEY (requested_by) REFERENCES users(id) ON DELETE RESTRICT,
  CONSTRAINT uq_refund_no UNIQUE (out_refund_no),
  CONSTRAINT uq_refund_provider UNIQUE (provider_refund_id),
  CONSTRAINT ck_refund_amount CHECK (amount_cents > 0),
  KEY idx_refunds_order (order_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS refund_items (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  refund_id BIGINT NOT NULL,
  order_item_id BIGINT NOT NULL,
  quantity INT NOT NULL,
  amount_cents BIGINT NOT NULL,
  CONSTRAINT fk_refund_item_refund FOREIGN KEY (refund_id) REFERENCES refunds(id) ON DELETE RESTRICT,
  CONSTRAINT fk_refund_item_order_item FOREIGN KEY (order_item_id) REFERENCES order_items(id) ON DELETE RESTRICT,
  CONSTRAINT uq_refund_item UNIQUE (refund_id, order_item_id),
  CONSTRAINT ck_refund_item_qty CHECK (quantity > 0 AND amount_cents >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS refund_cash_ledger (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  refund_id BIGINT NOT NULL,
  amount_cents BIGINT NOT NULL,
  occurred_at DATETIME(6) NOT NULL,
  source ENUM('WECHAT','SIMULATED') NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_cash_refund FOREIGN KEY (refund_id) REFERENCES refunds(id) ON DELETE RESTRICT,
  CONSTRAINT uq_cash_refund UNIQUE (refund_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS refund_restocks (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  refund_item_id BIGINT NOT NULL,
  lot_id BIGINT NOT NULL,
  quantity BIGINT NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_restock_refund_item FOREIGN KEY (refund_item_id) REFERENCES refund_items(id) ON DELETE RESTRICT,
  CONSTRAINT fk_restock_lot FOREIGN KEY (lot_id) REFERENCES inventory_lots(id) ON DELETE RESTRICT,
  CONSTRAINT uq_restock_item UNIQUE (refund_item_id),
  CONSTRAINT ck_restock_qty CHECK (quantity > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS delivery_tasks (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  order_id BIGINT NOT NULL,
  store_id BIGINT NOT NULL,
  assignee_id BIGINT NULL,
  status ENUM('OPEN','ACCEPTED','PICKED_UP','DELIVERING','COMPLETED','CANCELLED','EXCEPTION') NOT NULL DEFAULT 'OPEN',
  version BIGINT NOT NULL DEFAULT 1,
  assignment_generation INT NOT NULL DEFAULT 0,
  assigned_at DATETIME(6) NULL,
  accepted_at DATETIME(6) NULL,
  picked_at DATETIME(6) NULL,
  started_at DATETIME(6) NULL,
  completed_at DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_task_order FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT,
  CONSTRAINT fk_task_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  CONSTRAINT fk_task_assignee FOREIGN KEY (assignee_id) REFERENCES users(id) ON DELETE RESTRICT,
  CONSTRAINT uq_task_order UNIQUE (order_id),
  KEY idx_task_store_status (store_id, status, created_at, id),
  KEY idx_task_assignee (assignee_id, status, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS delivery_codes (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  task_id BIGINT NOT NULL,
  generation INT NOT NULL,
  code_hash CHAR(64) NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  used_at DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_code_task FOREIGN KEY (task_id) REFERENCES delivery_tasks(id) ON DELETE RESTRICT,
  CONSTRAINT uq_code_generation UNIQUE (task_id, generation)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS courier_capacities (
  store_id BIGINT NOT NULL,
  courier_id BIGINT NOT NULL,
  online TINYINT NOT NULL DEFAULT 1,
  active_task_count INT NOT NULL DEFAULT 0,
  task_limit INT NOT NULL DEFAULT 3,
  version BIGINT NOT NULL DEFAULT 1,
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (store_id, courier_id),
  CONSTRAINT fk_capacity_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  CONSTRAINT fk_capacity_user FOREIGN KEY (courier_id) REFERENCES users(id) ON DELETE RESTRICT,
  CONSTRAINT ck_capacity_count CHECK (active_task_count >= 0 AND active_task_count <= task_limit AND task_limit > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS stock_movements (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  store_id BIGINT NOT NULL,
  sku_id BIGINT NOT NULL,
  lot_id BIGINT NOT NULL,
  movement_type ENUM('RECEIPT','WASTE','RESERVE','RELEASE','SALE','REFUND_RESTOCK') NOT NULL,
  quantity BIGINT NOT NULL,
  unit_cost_cents BIGINT NOT NULL DEFAULT 0,
  ref_type VARCHAR(40) NOT NULL DEFAULT '',
  ref_id VARCHAR(64) NOT NULL DEFAULT '',
  actor_id BIGINT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_movement_store FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE RESTRICT,
  CONSTRAINT fk_movement_sku FOREIGN KEY (sku_id) REFERENCES skus(id) ON DELETE RESTRICT,
  CONSTRAINT fk_movement_lot FOREIGN KEY (lot_id) REFERENCES inventory_lots(id) ON DELETE RESTRICT,
  CONSTRAINT ck_movement_qty CHECK (quantity <> 0),
  KEY idx_movement_sku (store_id, sku_id, created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS order_events (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  order_id BIGINT NOT NULL,
  event_type VARCHAR(48) NOT NULL,
  actor_type VARCHAR(20) NOT NULL DEFAULT 'SYSTEM',
  actor_id BIGINT NULL,
  payload_json JSON NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_event_order FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT,
  KEY idx_events_order (order_id, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS outbox (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  topic VARCHAR(64) NOT NULL,
  payload_json JSON NOT NULL,
  status ENUM('PENDING','PROCESSING','DONE','FAILED') NOT NULL DEFAULT 'PENDING',
  attempts INT NOT NULL DEFAULT 0,
  next_run_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  processed_at DATETIME(6) NULL,
  KEY idx_outbox_pending (status, next_run_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS audit_logs (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  actor_id BIGINT NULL,
  store_id BIGINT NULL,
  action VARCHAR(64) NOT NULL,
  object_type VARCHAR(48) NOT NULL,
  object_id VARCHAR(64) NOT NULL,
  before_json JSON NULL,
  after_json JSON NULL,
  trace_id VARCHAR(64) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  KEY idx_audit_store (store_id, created_at, id),
  KEY idx_audit_object (object_type, object_id, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS login_attempts (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  identity VARCHAR(128) NOT NULL,
  ip VARCHAR(64) NOT NULL,
  success TINYINT NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  KEY idx_login_identity (identity, created_at),
  KEY idx_login_ip (ip, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
