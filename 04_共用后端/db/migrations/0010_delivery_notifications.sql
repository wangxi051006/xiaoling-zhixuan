-- Delivery notifications share the durable inbox; existing urge records remain intact.
ALTER TABLE notifications MODIFY urge_id BIGINT NULL;
SET @ddl = IF((SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='notifications' AND column_name='event_key')=0,
 'ALTER TABLE notifications ADD COLUMN event_key VARCHAR(100) NULL', 'SELECT 1');
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;
SET @ddl = IF((SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='notifications' AND index_name='uq_notification_event')=0,
 'ALTER TABLE notifications ADD UNIQUE KEY uq_notification_event (recipient_type,recipient_id,event_key)', 'SELECT 1');
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;
CREATE TABLE IF NOT EXISTS notification_recipient_locks (
 recipient_id BIGINT NOT NULL PRIMARY KEY
) ENGINE=InnoDB;

