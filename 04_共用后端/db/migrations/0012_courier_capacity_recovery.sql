-- Historical/imported workloads may exceed today's limit. Preserve the true
-- count so couriers can complete existing tasks; new claims are still limited
-- by the serialized application command. Add the remaining invariants before
-- removing the old upper-bound constraint so interrupted DDL stays safe.
SET @capacity_ddl = IF((SELECT COUNT(*) FROM information_schema.table_constraints
 WHERE constraint_schema=DATABASE() AND table_name='courier_capacities' AND constraint_name='ck_capacity_nonnegative')=0,
 'ALTER TABLE courier_capacities ADD CONSTRAINT ck_capacity_nonnegative CHECK (active_task_count >= 0 AND task_limit > 0)', 'SELECT 1');
PREPARE capacity_stmt FROM @capacity_ddl; EXECUTE capacity_stmt; DEALLOCATE PREPARE capacity_stmt;

SET @capacity_ddl = IF((SELECT COUNT(*) FROM information_schema.table_constraints
 WHERE constraint_schema=DATABASE() AND table_name='courier_capacities' AND constraint_name='ck_capacity_count')>0,
 'ALTER TABLE courier_capacities DROP CHECK ck_capacity_count', 'SELECT 1');
PREPARE capacity_stmt FROM @capacity_ddl; EXECUTE capacity_stmt; DEALLOCATE PREPARE capacity_stmt;
