SET @ddl = IF((SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='batch_operations' AND index_name='uq_batch_actor_request')=0,
 'ALTER TABLE batch_operations ADD UNIQUE KEY uq_batch_actor_request (actor_id,request_id)', 'SELECT 1');
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;
SET @ddl = IF((SELECT COUNT(*) FROM information_schema.table_constraints WHERE constraint_schema=DATABASE() AND table_name='batch_operation_items' AND constraint_name='fk_batch_item_target')=0,
 'ALTER TABLE batch_operation_items ADD CONSTRAINT fk_batch_item_target FOREIGN KEY (batch_id,order_id) REFERENCES batch_operation_targets(batch_id,order_id)', 'SELECT 1');
PREPARE s FROM @ddl; EXECUTE s; DEALLOCATE PREPARE s;

