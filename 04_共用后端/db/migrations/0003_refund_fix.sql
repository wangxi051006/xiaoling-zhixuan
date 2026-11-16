-- 0003：退款一致性与额度预占修复
-- 由 internal/db.Migrate 按列/索引检查定义后逐步执行，兼容本文件半执行。
-- 已登记旧 0003 的数据库由 0004 校验结构并恢复/隔离历史数据；勿手工跳过 0004。
-- 1) 订单行增加“处理中退款数量”，用于在事务内预占 REQUESTED/PROCESSING/UNKNOWN 的可退数量，
--    只有 SUCCESS 才转入 refunded_quantity；失败/关闭路径释放 pending。
-- 2) 退款主记录增加请求指纹，用于身份/订单/参数绑定的幂等重放与异参 409。
ALTER TABLE order_items
  ADD COLUMN pending_refund_quantity INT NOT NULL DEFAULT 0 AFTER refunded_quantity;

ALTER TABLE refunds
  ADD COLUMN request_hash CHAR(64) NULL AFTER after_sale_id,
  ADD KEY idx_refund_after_sale (after_sale_id, order_id, requested_by);
