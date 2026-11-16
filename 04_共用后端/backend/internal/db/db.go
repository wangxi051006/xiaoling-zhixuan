package db

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

func Open(dsn string) (*sql.DB, error) {
	dsn, err := normalizeUTCConfig(dsn)
	if err != nil {
		return nil, err
	}
	handle, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	handle.SetMaxOpenConns(40)
	handle.SetMaxIdleConns(20)
	handle.SetConnMaxLifetime(30 * time.Minute)
	handle.SetConnMaxIdleTime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := handle.PingContext(ctx); err != nil {
		handle.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	var zone string
	if err := handle.QueryRowContext(ctx, `SELECT @@session.time_zone`).Scan(&zone); err != nil {
		handle.Close()
		return nil, fmt.Errorf("verify mysql time zone: %w", err)
	}
	if zone != "+00:00" && !strings.EqualFold(zone, "UTC") && !strings.EqualFold(zone, "Etc/UTC") {
		handle.Close()
		return nil, fmt.Errorf("mysql session time_zone must be UTC, got %q", zone)
	}
	return handle, nil
}

func normalizeUTCConfig(dsn string) (string, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("parse mysql configuration: %w", err)
	}
	// DATETIME values and TIMESTAMP session conversion must agree. Keeping a
	// user-supplied driver loc while fixing only the server session shifts dates.
	cfg.Loc = time.UTC
	cfg.ParseTime = true
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	return cfg.FormatDSN(), nil
}

// Migrate applies db/migrations/*.sql exactly once, in lexical order. DDL is
// not transactional in MySQL, so each file is recorded only after all its
// statements succeed. Files must be written to be restart-safe (IF NOT EXISTS
// or an equivalent check) because a crash can leave a partially applied file.
func Migrate(database *sql.DB, dir string) error {
	ctx := context.Background()
	conn, err := database.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked int
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(CONCAT('retail_migrate_',LEFT(SHA2(DATABASE(),256),40)),30)`).Scan(&locked); err != nil || locked != 1 {
		return fmt.Errorf("acquire migration lock: %v (locked=%d)", err, locked)
	}
	defer conn.ExecContext(ctx, `DO RELEASE_LOCK(CONCAT('retail_migrate_',LEFT(SHA2(DATABASE(),256),40)))`)
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version VARCHAR(128) PRIMARY KEY,
		applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations dir %q: %w", dir, err)
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		files = append(files, entry.Name())
	}
	sort.Strings(files)
	for _, name := range files {
		var applied int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=?`, name).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied > 0 {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if strings.TrimSpace(string(body)) == "" {
			continue
		}
		var applyErr error
		switch name {
		case "0003_refund_fix.sql":
			applyErr = ensureRefundV3(ctx, conn)
		case "0004_refund_recovery.sql":
			applyErr = ensureRefundV4(ctx, conn, string(body))
		default:
			_, applyErr = conn.ExecContext(ctx, string(body))
		}
		if applyErr != nil {
			return fmt.Errorf("apply migration %s: %w", name, applyErr)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (?)`, name); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
	}
	return nil
}

// MySQL DDL commits implicitly. Check each definition before executing it so a
// restart after any individual ALTER resumes without dropping or resetting data.
func ensureColumn(ctx context.Context, c *sql.Conn, table, column, columnType, nullable, ddl string) error {
	var actualType, actualNullable string
	var actualDefault sql.NullString
	err := c.QueryRowContext(ctx, `SELECT COLUMN_TYPE,IS_NULLABLE,COLUMN_DEFAULT FROM information_schema.columns
		WHERE table_schema=DATABASE() AND table_name=? AND column_name=?`, table, column).Scan(&actualType, &actualNullable, &actualDefault)
	if err == sql.ErrNoRows {
		_, err = c.ExecContext(ctx, ddl)
		return err
	}
	if err != nil {
		return err
	}
	if !strings.EqualFold(actualType, columnType) || actualNullable != nullable {
		return fmt.Errorf("incompatible column %s.%s: %s nullable=%s", table, column, actualType, actualNullable)
	}
	if (column == "pending_refund_quantity" && (!actualDefault.Valid || actualDefault.String != "0")) || (nullable == "YES" && actualDefault.Valid) {
		return fmt.Errorf("incompatible default on %s.%s", table, column)
	}
	return nil
}

func ensureIndex(ctx context.Context, c *sql.Conn, table, name, columns string, unique bool, ddl string) error {
	var actual sql.NullString
	var nonUnique sql.NullInt64
	if err := c.QueryRowContext(ctx, `SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ','),MIN(non_unique)
		FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=?`, table, name).Scan(&actual, &nonUnique); err != nil {
		return err
	}
	if !actual.Valid {
		_, err := c.ExecContext(ctx, ddl)
		return err
	}
	if actual.String != columns || !nonUnique.Valid || (nonUnique.Int64 == 0) != unique {
		return fmt.Errorf("incompatible index %s.%s", table, name)
	}
	return nil
}

func ensureRefundV3(ctx context.Context, c *sql.Conn) error {
	if err := ensureColumn(ctx, c, "order_items", "pending_refund_quantity", "int", "NO",
		`ALTER TABLE order_items ADD COLUMN pending_refund_quantity INT NOT NULL DEFAULT 0 AFTER refunded_quantity`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, c, "refunds", "request_hash", "char(64)", "YES",
		`ALTER TABLE refunds ADD COLUMN request_hash CHAR(64) NULL AFTER after_sale_id`); err != nil {
		return err
	}
	return ensureIndex(ctx, c, "refunds", "idx_refund_after_sale", "after_sale_id,order_id,requested_by", false,
		`ALTER TABLE refunds ADD KEY idx_refund_after_sale(after_sale_id,order_id,requested_by)`)
}

func ensureRefundV4(ctx context.Context, c *sql.Conn, body string) error {
	// Also covers installations which recorded the original 0003 already.
	if err := ensureRefundV3(ctx, c); err != nil {
		return err
	}
	if _, err := c.ExecContext(ctx, body); err != nil {
		return err
	}
	for _, col := range []struct{ name, typ, ddl string }{
		{"request_id", "varchar(128)", `ALTER TABLE refund_restocks ADD COLUMN request_id VARCHAR(128) NULL`},
		{"request_hash", "char(64)", `ALTER TABLE refund_restocks ADD COLUMN request_hash CHAR(64) NULL`},
		{"actor_id", "bigint", "ALTER TABLE refund_restocks ADD COLUMN actor_id BIGINT NULL"},
	} {
		if err := ensureColumn(ctx, c, "refund_restocks", col.name, col.typ, "YES", col.ddl); err != nil {
			return err
		}
	}
	if err := ensureIndex(ctx, c, "refund_restocks", "idx_restock_item", "refund_item_id", false,
		`ALTER TABLE refund_restocks ADD KEY idx_restock_item(refund_item_id)`); err != nil {
		return err
	}
	if err := ensureIndex(ctx, c, "refund_restocks", "uq_restock_request", "actor_id,request_id", true,
		`ALTER TABLE refund_restocks ADD UNIQUE KEY uq_restock_request(actor_id,request_id)`); err != nil {
		return err
	}
	var oldIndex int
	if err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='refund_restocks' AND index_name='uq_restock_item'`).Scan(&oldIndex); err != nil {
		return err
	}
	if oldIndex > 0 {
		if _, err := c.ExecContext(ctx, `ALTER TABLE refund_restocks DROP INDEX uq_restock_item`); err != nil {
			return err
		}
	}
	var statusType string
	if err := c.QueryRowContext(ctx, `SELECT column_type FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='refunds' AND column_name='status'`).Scan(&statusType); err != nil {
		return err
	}
	if statusType == "enum('REQUESTED','PROCESSING','SUCCESS','FAILED','CLOSED')" {
		if _, err := c.ExecContext(ctx, `ALTER TABLE refunds MODIFY COLUMN status ENUM('REQUESTED','PROCESSING','SUCCESS','FAILED','CLOSED','UNKNOWN') NOT NULL DEFAULT 'REQUESTED'`); err != nil {
			return err
		}
	} else if statusType != "enum('REQUESTED','PROCESSING','SUCCESS','FAILED','CLOSED','UNKNOWN')" {
		return fmt.Errorf("incompatible refunds.status: %s", statusType)
	}
	return recoverRefundQuantities(ctx, c)
}

type recoveryItem struct{ quantity, refunded, pending, price, success, active int64 }

// Recovery never invents money or discards suspect records. A bad historical
// order is quarantined, keeping its original counters and evidence intact.
func recoverRefundQuantities(ctx context.Context, c *sql.Conn) error {
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT o.id FROM orders o JOIN refunds r ON r.order_id=o.id ORDER BY o.id`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range ids {
		if err := recoverRefundOrder(ctx, tx, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func recoverRefundOrder(ctx context.Context, tx *sql.Tx, orderID int64) error {
	var goods, storeID int64
	var payment string
	if err := tx.QueryRowContext(ctx, `SELECT goods_cents,store_id,payment_status FROM orders WHERE id=? FOR UPDATE`, orderID).Scan(&goods, &storeID, &payment); err != nil {
		return err
	}
	items := map[int64]*recoveryItem{}
	rows, err := tx.QueryContext(ctx, `SELECT id,quantity,refunded_quantity,pending_refund_quantity,unit_price_cents FROM order_items WHERE order_id=? ORDER BY id FOR UPDATE`, orderID)
	if err != nil {
		return err
	}
	reason := ""
	for rows.Next() {
		var id int64
		v := &recoveryItem{}
		if err := rows.Scan(&id, &v.quantity, &v.refunded, &v.pending, &v.price); err != nil {
			rows.Close()
			return err
		}
		items[id] = v
		if v.quantity <= 0 || v.price < 0 || v.refunded < 0 || v.pending < 0 || v.refunded > v.quantity || v.pending > v.quantity {
			reason = "INVALID_EXISTING_ITEM_COUNTER"
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	type refundRow struct {
		id, amount int64
		status     string
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,amount_cents,status FROM refunds WHERE order_id=? ORDER BY id FOR UPDATE`, orderID)
	if err != nil {
		return err
	}
	var refunds []refundRow
	for rows.Next() {
		var r refundRow
		if err := rows.Scan(&r.id, &r.amount, &r.status); err != nil {
			rows.Close()
			return err
		}
		refunds = append(refunds, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var successMoney, activeMoney int64
	for _, r := range refunds {
		rows, err = tx.QueryContext(ctx, `SELECT order_item_id,quantity,amount_cents FROM refund_items WHERE refund_id=? ORDER BY id FOR UPDATE`, r.id)
		if err != nil {
			return err
		}
		var sum, count int64
		for rows.Next() {
			var itemID, q, amount int64
			if err := rows.Scan(&itemID, &q, &amount); err != nil {
				rows.Close()
				return err
			}
			count++
			v, ok := items[itemID]
			if !ok || q <= 0 || amount < 0 || (v != nil && (q > v.quantity || (v.price > 0 && q > math.MaxInt64/v.price) || amount != q*v.price)) {
				reason = "INVALID_REFUND_DETAIL"
				continue
			}
			if sum > math.MaxInt64-amount {
				reason = "REFUND_AMOUNT_OVERFLOW"
				continue
			}
			sum += amount
			if r.status == "SUCCESS" {
				if v.success > v.quantity-q {
					reason = "REFUND_QUANTITY_EXCEEDED"
				} else {
					v.success += q
				}
			}
			if r.status == "REQUESTED" || r.status == "PROCESSING" || r.status == "UNKNOWN" {
				if v.active > v.quantity-q {
					reason = "REFUND_QUANTITY_EXCEEDED"
				} else {
					v.active += q
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if count == 0 || r.amount <= 0 || sum != r.amount {
			reason = "REFUND_MASTER_DETAIL_MISMATCH"
		}
		if r.status == "SUCCESS" {
			if r.amount > goods || successMoney > goods-r.amount {
				reason = "REFUND_AMOUNT_EXCEEDED"
			} else {
				successMoney += r.amount
			}
		} else if r.status == "REQUESTED" || r.status == "PROCESSING" || r.status == "UNKNOWN" {
			if r.amount > goods || activeMoney > goods-r.amount {
				reason = "REFUND_AMOUNT_EXCEEDED"
			} else {
				activeMoney += r.amount
			}
		}
		var ledgerCount, ledgerAmount int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(amount_cents),0) FROM refund_cash_ledger WHERE refund_id=?`, r.id).Scan(&ledgerCount, &ledgerAmount); err != nil {
			return err
		}
		if (r.status == "SUCCESS" && (ledgerCount != 1 || ledgerAmount != r.amount)) || (r.status != "SUCCESS" && ledgerCount != 0) {
			reason = "REFUND_CASH_LEDGER_MISMATCH"
		}
		var badRestock int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM refund_items ri JOIN order_items oi ON oi.id=ri.order_item_id
			WHERE ri.refund_id=? AND (COALESCE((SELECT SUM(rr.quantity) FROM refund_restocks rr WHERE rr.refund_item_id=ri.id),0)>ri.quantity
			OR EXISTS(SELECT 1 FROM refund_restocks rr JOIN inventory_lots l ON l.id=rr.lot_id WHERE rr.refund_item_id=ri.id AND (l.sku_id<>oi.sku_id OR l.store_id<>? OR ?<>'SUCCESS')))`, r.id, storeID, r.status).Scan(&badRestock); err != nil {
			return err
		}
		if badRestock > 0 {
			reason = "INVALID_HISTORICAL_RESTOCK"
		}
	}
	for _, v := range items {
		if v.success > v.quantity-v.active {
			reason = "REFUND_QUANTITY_EXCEEDED"
		}
		if v.refunded != v.success || (v.pending != 0 && v.pending != v.active) {
			reason = "EXISTING_REFUND_COUNTER_MISMATCH"
		}
	}
	if successMoney > goods-activeMoney {
		reason = "REFUND_AMOUNT_EXCEEDED"
	}
	if payment != "PAID" && payment != "PARTIAL_REFUNDED" && payment != "REFUNDED" {
		reason = "INVALID_REFUND_PAYMENT_STATE"
	}
	if reason != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO refund_recovery_blocks(order_id,reason) VALUES (?,?) ON DUPLICATE KEY UPDATE order_id=order_id`, orderID, reason)
		return err
	}
	// Existing blocks are deliberately not auto-cleared by a migration retry.
	var blocked int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM refund_recovery_blocks WHERE order_id=?`, orderID).Scan(&blocked); err != nil {
		return err
	}
	if blocked > 0 {
		return nil
	}
	for id, v := range items {
		if _, err := tx.ExecContext(ctx, `UPDATE order_items SET refunded_quantity=?,pending_refund_quantity=? WHERE id=?`, v.success, v.active, id); err != nil {
			return err
		}
	}
	next := "PAID"
	if successMoney > 0 {
		next = "PARTIAL_REFUNDED"
		if successMoney == goods {
			next = "REFUNDED"
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE orders SET payment_status=? WHERE id=? AND payment_status<>?`, next, orderID, next)
	return err
}
