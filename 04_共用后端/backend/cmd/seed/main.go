package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"campusretail/internal/app"
	"campusretail/internal/config"
	"campusretail/internal/db"
)

// Seed creates an explicitly-labelled local demo dataset. It never runs in
// production and never overwrites a real order (orders are matched by their
// stable DEMO order number and skipped when present).
func main() {
	cfg, err := config.Load()
	if err != nil {
		fail("configuration", err)
	}
	if cfg.Env == "production" {
		fail("seed", fmt.Errorf("SEED_DEMO is forbidden in production"))
	}
	if cfg.StoreID != 1 {
		fail("seed", fmt.Errorf("local demo seed currently uses STORE_ID=1"))
	}
	database, err := db.Open(cfg.MySQLDSN)
	if err != nil {
		fail("mysql", err)
	}
	defer database.Close()
	if err := db.Migrate(database, cfg.MigrationsDir); err != nil {
		fail("migrate", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		fail("begin", err)
	}
	defer tx.Rollback()
	if err := seedCatalog(ctx, tx, cfg); err != nil {
		fail("catalog seed", err)
	}
	if err := seedAccounts(ctx, tx, cfg); err != nil {
		fail("account seed", err)
	}
	if err := seedDemoOrders(ctx, tx); err != nil {
		fail("order seed", err)
	}
	if err := tx.Commit(); err != nil {
		fail("commit", err)
	}
	slog.Info("demo seed complete; all seeded orders are marked is_demo=1",
		"login", "13800000001", "passwordEnv", "DEMO_PASSWORD")
}

func fail(stage string, err error) {
	slog.Error("seed failed", "stage", stage, "error", err)
	os.Exit(1)
}

func seedCatalog(ctx context.Context, tx *sql.Tx, cfg config.Config) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO stores(id,name) VALUES (1,'校零智选·紫荆店')
		ON DUPLICATE KEY UPDATE name=VALUES(name)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO urge_policies(store_id,cooldown_seconds,max_per_order,enabled,version)
		VALUES (?,300,3,1,1) ON DUPLICATE KEY UPDATE cooldown_seconds=VALUES(cooldown_seconds),max_per_order=VALUES(max_per_order),enabled=1`,
		cfg.StoreID); err != nil {
		return err
	}
	buildings := []struct {
		id   int64
		name string
		fee  int64
	}{
		{1, "紫荆公寓", 200}, {2, "梧桐苑", 200}, {3, "银杏苑", 200}, {4, "校外东门", 300},
	}
	for _, building := range buildings {
		if _, err := tx.ExecContext(ctx, `INSERT INTO buildings(id,store_id,name,delivery_fee_cents,active)
			VALUES (?,?,?,?,1) ON DUPLICATE KEY UPDATE name=VALUES(name),delivery_fee_cents=VALUES(delivery_fee_cents)`,
			building.id, cfg.StoreID, building.name, building.fee); err != nil {
			return err
		}
	}
	categories := []struct {
		id    int64
		name  string
		order int
	}{{1, "零食", 1}, {2, "饮料", 2}, {3, "烘焙", 3}, {4, "速食", 4}}
	for _, category := range categories {
		if _, err := tx.ExecContext(ctx, `INSERT INTO categories(id,store_id,name,sort_order,active)
			VALUES (?,?,?,?,1) ON DUPLICATE KEY UPDATE name=VALUES(name),sort_order=VALUES(sort_order)`,
			category.id, cfg.StoreID, category.name, category.order); err != nil {
			return err
		}
	}
	products := []struct {
		id, category int64
		name, desc   string
	}{
		{1, 1, "爆浆芝士肠", "校园热卖，咸香小食"},
		{2, 2, "无糖乌龙茶", "500ml 冷饮"},
		{3, 3, "黄油曲奇", "宿舍分享装"},
		{4, 2, "燕麦奶", "早八轻负担"},
		{5, 1, "麻辣牛肉干", "微辣耐嚼"},
		{6, 1, "每日坚果", "7 小袋装"},
		{7, 2, "冰镇气泡水", "青柠味"},
		{8, 4, "关东煮套餐", "晚间热食"},
	}
	for _, product := range products {
		if _, err := tx.ExecContext(ctx, `INSERT INTO products(id,store_id,category_id,name,description,active,is_demo)
			VALUES (?,?,?,?,?,1,1) ON DUPLICATE KEY UPDATE name=VALUES(name),description=VALUES(description),category_id=VALUES(category_id)`,
			product.id, cfg.StoreID, product.category, product.name, product.desc); err != nil {
			return err
		}
	}
	skus := []struct {
		id, product, price int64
		spec               string
	}{
		{1, 1, 650, "70g"}, {9, 1, 1200, "140g"}, {2, 2, 500, "500ml"}, {10, 2, 900, "1L"},
		{3, 3, 890, "120g"}, {4, 4, 450, "250ml"}, {5, 5, 300, "50g"},
		{6, 6, 990, "7袋"}, {7, 7, 400, "330ml"}, {8, 8, 1200, "套餐"},
	}
	for _, sku := range skus {
		if _, err := tx.ExecContext(ctx, `INSERT INTO skus(id,store_id,product_id,spec_name,price_cents,active)
			VALUES (?,?,?,?,?,1) ON DUPLICATE KEY UPDATE spec_name=VALUES(spec_name),price_cents=VALUES(price_cents)`,
			sku.id, cfg.StoreID, sku.product, sku.spec, sku.price); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO inventory_lots(id,store_id,sku_id,batch_no,expiry_date,qty_on_hand,qty_reserved,unit_cost_cents)
			VALUES (?,?,?,?,?,100,0,?) ON DUPLICATE KEY UPDATE batch_no=VALUES(batch_no)`,
			sku.id, cfg.StoreID, sku.id, "DEMO-B"+fmt.Sprint(sku.id), "2027-03-31", sku.price*60/100); err != nil {
			return err
		}
	}
	return nil
}

func seedAccounts(ctx context.Context, tx *sql.Tx, cfg config.Config) error {
	passwordHash, err := app.HashPassword(cfg.DemoPassword)
	if err != nil {
		return err
	}
	users := []struct {
		id       int64
		phone    string
		nickname string
	}{
		{1, "13800000001", "店长"}, {2, "13800000002", "备货员"}, {3, "13800000003", "调度员"},
		{4, "13800000004", "配送员"}, {5, "13800000005", "财务"}, {6, "13900000001", "张同学"},
		{7, "13900000002", "李同学"}, {8, "13900000003", "王同学"},
	}
	for _, user := range users {
		if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,phone,nickname,password_hash,status,is_demo,default_address_id)
			VALUES (?,?,?,?, 'ACTIVE',1,NULL) ON DUPLICATE KEY UPDATE nickname=VALUES(nickname),password_hash=VALUES(password_hash),is_demo=1`,
			user.id, user.phone, user.nickname, passwordHash); err != nil {
			return err
		}
	}
	staff := []struct {
		id, user int64
		role     string
	}{
		{1, 1, "store_admin"}, {2, 2, "warehouse_operator"}, {3, 3, "dispatcher"},
		{4, 4, "courier"}, {5, 5, "finance"},
	}
	for _, member := range staff {
		if _, err := tx.ExecContext(ctx, `INSERT INTO staff_memberships(id,user_id,store_id,active,version)
			VALUES (?,?,?,1,1) ON DUPLICATE KEY UPDATE active=1`,
			member.id, member.user, cfg.StoreID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO staff_roles(membership_id,role) VALUES (?,?)`,
			member.id, member.role); err != nil {
			return err
		}
	}
	addresses := []struct {
		id, owner, building int64
		name, phone, detail string
	}{
		{1, 6, 1, "张同学", "13900000001", "6号楼512"},
		{2, 7, 2, "李同学", "13900000002", "3号楼208"},
		{3, 8, 3, "王同学", "13900000003", "1号楼907"},
	}
	for _, address := range addresses {
		if _, err := tx.ExecContext(ctx, `INSERT INTO addresses(id,owner_id,store_id,building_id,building_name_snapshot,contact_name,phone,detail)
			VALUES (?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE contact_name=VALUES(contact_name),phone=VALUES(phone),detail=VALUES(detail)`,
			address.id, address.owner, cfg.StoreID, address.building, buildingName(address.building), address.name, address.phone, address.detail); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET default_address_id=? WHERE id=?`, address.id, address.owner); err != nil {
			return err
		}
	}
	return nil
}

func buildingName(id int64) string {
	switch id {
	case 1:
		return "紫荆公寓"
	case 2:
		return "梧桐苑"
	case 3:
		return "银杏苑"
	default:
		return "校外东门"
	}
}

type demoItem struct {
	skuID       int64
	productName string
	spec        string
	unitCents   int64
	quantity    int
}

type demoOrder struct {
	orderNo     string
	buyerID     int64
	ordinal     int
	paidAt      time.Time
	addressID   int64
	items       []demoItem
	delivery    int64
	refundSKU   int64
	refundQty   int
	refundCents int64
	active      bool
}

func seedDemoOrders(ctx context.Context, tx *sql.Tx) error {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		shanghai = time.FixedZone("CST", 8*3600)
	}
	orders := []demoOrder{
		{orderNo: "DEMO2026111201", buyerID: 6, ordinal: 1, paidAt: time.Date(2026, 11, 12, 10, 5, 0, 0, shanghai), addressID: 1,
			items: []demoItem{{1, "爆浆芝士肠", "70g", 650, 2}, {2, "无糖乌龙茶", "500ml", 500, 1}}, delivery: 200},
		{orderNo: "DEMO2026111202", buyerID: 6, ordinal: 2, paidAt: time.Date(2026, 11, 12, 15, 10, 0, 0, shanghai), addressID: 1,
			items: []demoItem{{3, "黄油曲奇", "120g", 890, 1}, {4, "燕麦奶", "250ml", 450, 1}}, delivery: 200},
		{orderNo: "DEMO2026111301", buyerID: 7, ordinal: 1, paidAt: time.Date(2026, 11, 13, 11, 20, 0, 0, shanghai), addressID: 2,
			items: []demoItem{{1, "爆浆芝士肠", "70g", 650, 1}, {3, "黄油曲奇", "120g", 890, 2}}, delivery: 200,
			refundSKU: 3, refundQty: 1, refundCents: 890},
		{orderNo: "DEMO2026111302", buyerID: 8, ordinal: 1, paidAt: time.Date(2026, 11, 13, 16, 40, 0, 0, shanghai), addressID: 3,
			items: []demoItem{{2, "无糖乌龙茶", "500ml", 500, 3}, {4, "燕麦奶", "250ml", 450, 1}}, delivery: 200},
		{orderNo: "DEMO2026111303", buyerID: 6, ordinal: 3, paidAt: time.Now().In(shanghai).Add(-20 * time.Minute), addressID: 1,
			items: []demoItem{{1, "爆浆芝士肠", "70g", 650, 1}, {4, "燕麦奶", "250ml", 450, 1}}, delivery: 200, active: true},
	}
	adminID := int64(1)
	courierID := int64(4)
	// Share the live dispatch serialization point before adding demo tasks.
	if _, err := tx.ExecContext(ctx, `INSERT INTO courier_capacities(store_id,courier_id,online,active_task_count,task_limit) VALUES (1,?,1,0,3) ON DUPLICATE KEY UPDATE courier_id=courier_id`, courierID); err != nil {
		return err
	}
	for _, order := range orders {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE order_no=?`, order.orderNo).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		goods := int64(0)
		for _, item := range order.items {
			goods += item.unitCents * int64(item.quantity)
		}
		total := goods + order.delivery
		addressJSON, _ := json.Marshal(map[string]any{
			"buildingId": fmt.Sprint(order.addressID), "buildingName": buildingName(order.addressID),
			"contactName": "演示同学", "phone": "13900000000", "detail": "演示门牌",
		})
		expires := order.paidAt.Add(10 * time.Minute)
		orderStatus, paymentStatus, deliveryStatus := "COMPLETED", "PAID", "DELIVERED"
		taskStatus := "COMPLETED"
		completedAt := any(order.paidAt.Add(30 * time.Minute).UTC())
		if order.active {
			orderStatus, paymentStatus, deliveryStatus = "DELIVERING", "PAID", "DELIVERING"
			taskStatus = "DELIVERING"
			completedAt = nil
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO orders
			(order_no,store_id,buyer_id,request_id,request_hash,status,payment_status,delivery_status,
			 goods_cents,delivery_cents,discount_cents,total_cents,currency,address_json,remark,expires_at,paid_at,
			 customer_order_ordinal,is_first_order,is_demo)
			VALUES (?,?,?,?,?,?,?,?,?,?,0,?,'CNY',?,'演示种子',?,?,?,?,1)`,
			order.orderNo, 1, order.buyerID, "seed_"+order.orderNo, strings.Repeat("d", 64),
			orderStatus, paymentStatus, deliveryStatus,
			goods, order.delivery, total, addressJSON, expires, order.paidAt.UTC(),
			order.ordinal, boolInt(order.ordinal == 1))
		if err != nil {
			return err
		}
		orderID, _ := result.LastInsertId()
		itemIDs := map[int64]int64{}
		for _, item := range order.items {
			itemResult, err := tx.ExecContext(ctx, `INSERT INTO order_items(order_id,sku_id,product_name,spec_name,unit_price_cents,quantity,goods_cents,refunded_quantity)
				VALUES (?,?,?,?,?,?,?,0)`, orderID, item.skuID, item.productName, item.spec, item.unitCents, item.quantity, item.unitCents*int64(item.quantity))
			if err != nil {
				return err
			}
			itemID, _ := itemResult.LastInsertId()
			itemIDs[item.skuID] = itemID
		}
		attemptResult, err := tx.ExecContext(ctx, `INSERT INTO payment_attempts
			(order_id,mchid,appid,out_trade_no,channel,amount_cents,currency,status,transaction_id,paid_at)
			VALUES (?,?,?,?, 'SIMULATED',?,'CNY','PAID',?,?)`,
			orderID, "SIMULATED", "SIMULATED", "SIM"+order.orderNo, total, "SIMULATED-"+order.orderNo, order.paidAt.UTC())
		if err != nil {
			return err
		}
		attemptID, _ := attemptResult.LastInsertId()
		if _, err := tx.ExecContext(ctx, `INSERT INTO payment_transactions(attempt_id,order_id,transaction_id,amount_cents,paid_at,source,raw_digest)
			VALUES (?,?,?,?,?, 'SIMULATED',?)`, attemptID, orderID, "SIMULATED-"+order.orderNo, total, order.paidAt.UTC(), strings.Repeat("e", 64)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_tasks(order_id,store_id,assignee_id,status,version,assignment_generation,
			assigned_at,accepted_at,picked_at,started_at,completed_at)
			VALUES (?,?,?,?,2,1,?,?,?,?,?)`, orderID, 1, courierID, taskStatus, order.paidAt.UTC(), order.paidAt.UTC(),
			order.paidAt.Add(5*time.Minute).UTC(), order.paidAt.Add(10*time.Minute).UTC(), completedAt); err != nil {
			return err
		}
		if order.refundSKU > 0 {
			itemID := itemIDs[order.refundSKU]
			refundResult, err := tx.ExecContext(ctx, `INSERT INTO refunds(order_id,out_refund_no,amount_cents,currency,status,reason,requested_by,provider_refund_id,succeeded_at)
				VALUES (?,?,?, 'CNY','SUCCESS','演示退款',?,?,?)`, orderID, "DEMO-R-"+order.orderNo, order.refundCents, adminID, "SIM-REF-"+order.orderNo, order.paidAt.Add(2*time.Hour).UTC())
			if err != nil {
				return err
			}
			refundID, _ := refundResult.LastInsertId()
			if _, err := tx.ExecContext(ctx, `INSERT INTO refund_items(refund_id,order_item_id,quantity,amount_cents) VALUES (?,?,?,?)`,
				refundID, itemID, order.refundQty, order.refundCents); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE order_items SET refunded_quantity=? WHERE id=?`, order.refundQty, itemID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE orders SET payment_status='PARTIAL_REFUNDED' WHERE id=?`, orderID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO refund_cash_ledger(refund_id,amount_cents,occurred_at,source)
				VALUES (?,?,?, 'SIMULATED')`, refundID, order.refundCents, order.paidAt.Add(2*time.Hour).UTC()); err != nil {
				return err
			}
		}
		_, _ = tx.ExecContext(ctx, `INSERT INTO order_events(order_id,event_type,actor_type,payload_json) VALUES (?,'ORDER_SEEDED','SYSTEM',?)`,
			orderID, mustJSON(map[string]any{"demo": true, "orderNo": order.orderNo}))
	}
	for buyerID, total := range map[int64]int64{6: 3, 7: 1, 8: 1} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO customer_order_counters(user_id,total_created) VALUES (?,?)
			ON DUPLICATE KEY UPDATE total_created=GREATEST(total_created,VALUES(total_created))`, buyerID, total); err != nil {
			return err
		}
	}
	active, err := app.CountActiveCourierTasks(ctx, tx, 1, courierID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE courier_capacities SET active_task_count=?,version=version+1 WHERE store_id=1 AND courier_id=?`, active, courierID); err != nil {
		return err
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func mustJSON(value any) []byte {
	body, _ := json.Marshal(value)
	return body
}
