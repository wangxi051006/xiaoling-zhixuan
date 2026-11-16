-- Explicit merchant-configured WGS84 points. One default point per building in
-- this release; no coordinates are inferred from addresses or seeded as real.
CREATE TABLE IF NOT EXISTS delivery_points (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  store_id BIGINT NOT NULL,
  building_id BIGINT NOT NULL,
  name VARCHAR(80) NOT NULL,
  latitude DECIMAL(10,7) NOT NULL,
  longitude DECIMAL(10,7) NOT NULL,
  radius_meters INT NOT NULL,
  active TINYINT NOT NULL DEFAULT 1,
  version BIGINT NOT NULL DEFAULT 1,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT uq_delivery_point_building UNIQUE(store_id,building_id),
  CONSTRAINT fk_delivery_point_store FOREIGN KEY(store_id) REFERENCES stores(id),
  CONSTRAINT fk_delivery_point_building FOREIGN KEY(building_id) REFERENCES buildings(id),
  CONSTRAINT ck_delivery_point_latitude CHECK(latitude BETWEEN -90 AND 90),
  CONSTRAINT ck_delivery_point_longitude CHECK(longitude BETWEEN -180 AND 180),
  CONSTRAINT ck_delivery_point_radius CHECK(radius_meters BETWEEN 20 AND 300)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Courier batches deliberately have separate ownership and result tables from
-- merchant batches. Each item result commits with the delivery state change.
CREATE TABLE IF NOT EXISTS courier_batches (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  store_id BIGINT NOT NULL,
  courier_id BIGINT NOT NULL,
  request_id VARCHAR(128) NOT NULL,
  action VARCHAR(16) NOT NULL,
  request_hash CHAR(64) NOT NULL,
  checked TINYINT NOT NULL DEFAULT 0,
  arrival_token TEXT NOT NULL,
  expected_count INT NOT NULL,
  status ENUM('RUNNING','DONE') NOT NULL DEFAULT 'RUNNING',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT uq_courier_batch_request UNIQUE(store_id,courier_id,request_id),
  CONSTRAINT ck_courier_batch_size CHECK(expected_count BETWEEN 1 AND 20),
  KEY idx_courier_batch_owner(store_id,courier_id,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS courier_batch_items (
  batch_id BIGINT NOT NULL,
  task_id BIGINT NOT NULL,
  expected_version BIGINT NOT NULL,
  result ENUM('PENDING','SUCCESS','FAILED') NOT NULL DEFAULT 'PENDING',
  error_code VARCHAR(40) NOT NULL DEFAULT '',
  message VARCHAR(200) NOT NULL DEFAULT '',
  version_after BIGINT NOT NULL DEFAULT 0,
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY(batch_id,task_id),
  CONSTRAINT fk_courier_batch_item FOREIGN KEY(batch_id) REFERENCES courier_batches(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
