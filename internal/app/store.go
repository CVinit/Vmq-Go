package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var ErrNotFound = errors.New("not found")

type Store interface {
	BootstrapDefaults(ctx context.Context, now time.Time, cfg Config) error
	GetSettings(ctx context.Context) (map[string]string, error)
	GetSetting(ctx context.Context, key string) (string, error)
	UpsertSettings(ctx context.Context, values map[string]string) error
	ReservePrice(ctx context.Context, key string) (bool, error)
	ReleasePrice(ctx context.Context, key string) error
	CreateOrder(ctx context.Context, order *PayOrder) error
	UpdateOrder(ctx context.Context, order *PayOrder) error
	GetOrderByPayID(ctx context.Context, payID string) (*PayOrder, error)
	GetOrderByOrderID(ctx context.Context, orderID string) (*PayOrder, error)
	GetOrderByID(ctx context.Context, id int64) (*PayOrder, error)
	GetOrderByPayDate(ctx context.Context, payDate int64) (*PayOrder, error)
	GetOpenOrderByPrice(ctx context.Context, reallyPrice float64, payType int) (*PayOrder, error)
	MarkOrderPaidByPrice(ctx context.Context, reallyPrice float64, payType int, payDate, closeDate int64) (*PayOrder, error)
	ListOrders(ctx context.Context, page, limit int, filter OrderFilter) ([]PayOrder, int64, error)
	DeleteOrder(ctx context.Context, id int64) error
	DeleteOrdersByState(ctx context.Context, state int) error
	DeleteOrdersBeforeCreateDate(ctx context.Context, before int64) error
	CreateQRCode(ctx context.Context, code *PayQRCode) error
	DeleteQRCode(ctx context.Context, id int64) error
	GetQRCodeByPriceAndType(ctx context.Context, price float64, payType int) (*PayQRCode, error)
	ListQRCodes(ctx context.Context, page, limit int, typeFilter *int) ([]PayQRCode, int64, error)
	GetDashboardStats(ctx context.Context, start, end int64) (DashboardStats, error)
	ExpireOrders(ctx context.Context, deadline, closeTime int64) ([]PayOrder, error)
}

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(databaseURL string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	store := &PostgresStore{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS settings (
			vkey TEXT PRIMARY KEY,
			vvalue TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS pay_orders (
			id BIGSERIAL PRIMARY KEY,
			order_id TEXT NOT NULL UNIQUE,
			pay_id TEXT NOT NULL UNIQUE,
			create_date BIGINT NOT NULL,
			pay_date BIGINT NOT NULL DEFAULT 0,
			close_date BIGINT NOT NULL DEFAULT 0,
			param TEXT NOT NULL DEFAULT '',
			type INTEGER NOT NULL,
			price NUMERIC(12,2) NOT NULL,
			really_price NUMERIC(12,2) NOT NULL,
			notify_url TEXT NOT NULL DEFAULT '',
			return_url TEXT NOT NULL DEFAULT '',
			state INTEGER NOT NULL,
			is_auto INTEGER NOT NULL,
			pay_url TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pay_orders_state_type_price ON pay_orders(state, type, really_price)`,
		`CREATE INDEX IF NOT EXISTS idx_pay_orders_create_date ON pay_orders(create_date)`,
		`CREATE TABLE IF NOT EXISTS pay_qrcodes (
			id BIGSERIAL PRIMARY KEY,
			pay_url TEXT NOT NULL,
			price NUMERIC(12,2) NOT NULL,
			type INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pay_qrcodes_type_price ON pay_qrcodes(type, price)`,
		`CREATE TABLE IF NOT EXISTS tmp_prices (
			price TEXT PRIMARY KEY
		)`,
	}

	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}

	return nil
}

func (s *PostgresStore) BootstrapDefaults(ctx context.Context, now time.Time, cfg Config) error {
	defaults, err := defaultSettings(now, cfg)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for key, value := range defaults {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO settings (vkey, vvalue) VALUES ($1, $2)
			ON CONFLICT (vkey) DO NOTHING
		`, key, value); err != nil {
			return err
		}
	}

	current, err := querySettingsTx(ctx, tx)
	if err != nil {
		return err
	}
	fixes := make(map[string]string)
	if current["deviceKey"] == "" || current["deviceKey"] == current["key"] {
		fixes["deviceKey"] = defaults["deviceKey"]
	}
	if len(fixes) > 0 {
		for key, value := range fixes {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO settings (vkey, vvalue) VALUES ($1, $2)
				ON CONFLICT (vkey) DO UPDATE SET vvalue = EXCLUDED.vvalue
			`, key, value); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func (s *PostgresStore) GetSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT vkey, vvalue FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var key string
		var value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		settings[key] = value
	}
	return settings, rows.Err()
}

func (s *PostgresStore) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT vvalue FROM settings WHERE vkey = $1`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return value, err
}

func (s *PostgresStore) UpsertSettings(ctx context.Context, values map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for key, value := range values {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO settings (vkey, vvalue) VALUES ($1, $2)
			ON CONFLICT (vkey) DO UPDATE SET vvalue = EXCLUDED.vvalue
		`, key, value); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *PostgresStore) ReservePrice(ctx context.Context, key string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `INSERT INTO tmp_prices (price) VALUES ($1) ON CONFLICT DO NOTHING`, key)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (s *PostgresStore) ReleasePrice(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tmp_prices WHERE price = $1`, key)
	return err
}

func (s *PostgresStore) CreateOrder(ctx context.Context, order *PayOrder) error {
	return s.db.QueryRowContext(ctx, `
		INSERT INTO pay_orders (
			order_id, pay_id, create_date, pay_date, close_date, param, type, price,
			really_price, notify_url, return_url, state, is_auto, pay_url
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14
		)
		RETURNING id
	`,
		order.OrderID, order.PayID, order.CreateDate, order.PayDate, order.CloseDate, order.Param,
		order.Type, round2(order.Price), round2(order.ReallyPrice), order.NotifyURL, order.ReturnURL,
		order.State, order.IsAuto, order.PayURL,
	).Scan(&order.ID)
}

func (s *PostgresStore) UpdateOrder(ctx context.Context, order *PayOrder) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE pay_orders
		SET order_id = $2,
			pay_id = $3,
			create_date = $4,
			pay_date = $5,
			close_date = $6,
			param = $7,
			type = $8,
			price = $9,
			really_price = $10,
			notify_url = $11,
			return_url = $12,
			state = $13,
			is_auto = $14,
			pay_url = $15
		WHERE id = $1
	`,
		order.ID, order.OrderID, order.PayID, order.CreateDate, order.PayDate, order.CloseDate, order.Param,
		order.Type, round2(order.Price), round2(order.ReallyPrice), order.NotifyURL, order.ReturnURL,
		order.State, order.IsAuto, order.PayURL,
	)
	return err
}

func (s *PostgresStore) GetOrderByPayID(ctx context.Context, payID string) (*PayOrder, error) {
	return s.getOrder(ctx, `SELECT id, order_id, pay_id, create_date, pay_date, close_date, param, type, price, really_price, notify_url, return_url, state, is_auto, pay_url FROM pay_orders WHERE pay_id = $1`, payID)
}

func (s *PostgresStore) GetOrderByOrderID(ctx context.Context, orderID string) (*PayOrder, error) {
	return s.getOrder(ctx, `SELECT id, order_id, pay_id, create_date, pay_date, close_date, param, type, price, really_price, notify_url, return_url, state, is_auto, pay_url FROM pay_orders WHERE order_id = $1`, orderID)
}

func (s *PostgresStore) GetOrderByID(ctx context.Context, id int64) (*PayOrder, error) {
	return s.getOrder(ctx, `SELECT id, order_id, pay_id, create_date, pay_date, close_date, param, type, price, really_price, notify_url, return_url, state, is_auto, pay_url FROM pay_orders WHERE id = $1`, id)
}

func (s *PostgresStore) GetOrderByPayDate(ctx context.Context, payDate int64) (*PayOrder, error) {
	return s.getOrder(ctx, `SELECT id, order_id, pay_id, create_date, pay_date, close_date, param, type, price, really_price, notify_url, return_url, state, is_auto, pay_url FROM pay_orders WHERE pay_date = $1`, payDate)
}

func (s *PostgresStore) GetOpenOrderByPrice(ctx context.Context, reallyPrice float64, payType int) (*PayOrder, error) {
	return s.getOrder(ctx, `SELECT id, order_id, pay_id, create_date, pay_date, close_date, param, type, price, really_price, notify_url, return_url, state, is_auto, pay_url FROM pay_orders WHERE really_price = $1 AND state = 0 AND type = $2 ORDER BY id DESC LIMIT 1`, round2(reallyPrice), payType)
}

func (s *PostgresStore) MarkOrderPaidByPrice(ctx context.Context, reallyPrice float64, payType int, payDate, closeDate int64) (*PayOrder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	order := PayOrder{}
	err = tx.QueryRowContext(ctx, `
		SELECT id, order_id, pay_id, create_date, pay_date, close_date, param, type, price, really_price, notify_url, return_url, state, is_auto, pay_url
		FROM pay_orders
		WHERE really_price = $1 AND state = 0 AND type = $2
		ORDER BY id DESC
		LIMIT 1
		FOR UPDATE
	`, round2(reallyPrice), payType).Scan(
		&order.ID,
		&order.OrderID,
		&order.PayID,
		&order.CreateDate,
		&order.PayDate,
		&order.CloseDate,
		&order.Param,
		&order.Type,
		&order.Price,
		&order.ReallyPrice,
		&order.NotifyURL,
		&order.ReturnURL,
		&order.State,
		&order.IsAuto,
		&order.PayURL,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE pay_orders SET state = 1, pay_date = $2, close_date = $3 WHERE id = $1 AND state = 0`, order.ID, payDate, closeDate); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tmp_prices WHERE price = $1`, priceKey(order.Type, order.ReallyPrice)); err != nil {
		return nil, err
	}

	order.State = 1
	order.PayDate = payDate
	order.CloseDate = closeDate
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &order, nil
}

func (s *PostgresStore) getOrder(ctx context.Context, query string, args ...any) (*PayOrder, error) {
	order := PayOrder{}
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&order.ID,
		&order.OrderID,
		&order.PayID,
		&order.CreateDate,
		&order.PayDate,
		&order.CloseDate,
		&order.Param,
		&order.Type,
		&order.Price,
		&order.ReallyPrice,
		&order.NotifyURL,
		&order.ReturnURL,
		&order.State,
		&order.IsAuto,
		&order.PayURL,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func (s *PostgresStore) ListOrders(ctx context.Context, page, limit int, filter OrderFilter) ([]PayOrder, int64, error) {
	clauses := []string{}
	args := []any{}
	next := 1
	if filter.Type != nil {
		clauses = append(clauses, fmt.Sprintf("type = $%d", next))
		args = append(args, *filter.Type)
		next++
	}
	if filter.State != nil {
		clauses = append(clauses, fmt.Sprintf("state = $%d", next))
		args = append(args, *filter.State)
		next++
	}

	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}

	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pay_orders`+where, args...).Scan(&count); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, (page-1)*limit)
	query := `SELECT id, order_id, pay_id, create_date, pay_date, close_date, param, type, price, really_price, notify_url, return_url, state, is_auto, pay_url FROM pay_orders` + where + fmt.Sprintf(` ORDER BY id DESC LIMIT $%d OFFSET $%d`, next, next+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	orders := make([]PayOrder, 0)
	for rows.Next() {
		order := PayOrder{}
		if err := rows.Scan(
			&order.ID,
			&order.OrderID,
			&order.PayID,
			&order.CreateDate,
			&order.PayDate,
			&order.CloseDate,
			&order.Param,
			&order.Type,
			&order.Price,
			&order.ReallyPrice,
			&order.NotifyURL,
			&order.ReturnURL,
			&order.State,
			&order.IsAuto,
			&order.PayURL,
		); err != nil {
			return nil, 0, err
		}
		orders = append(orders, order)
	}

	return orders, count, rows.Err()
}

func (s *PostgresStore) DeleteOrder(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pay_orders WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) DeleteOrdersByState(ctx context.Context, state int) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pay_orders WHERE state = $1`, state)
	return err
}

func (s *PostgresStore) DeleteOrdersBeforeCreateDate(ctx context.Context, before int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pay_orders WHERE create_date < $1`, before)
	return err
}

func (s *PostgresStore) CreateQRCode(ctx context.Context, code *PayQRCode) error {
	return s.db.QueryRowContext(ctx, `INSERT INTO pay_qrcodes (pay_url, price, type) VALUES ($1, $2, $3) RETURNING id`, code.PayURL, round2(code.Price), code.Type).Scan(&code.ID)
}

func (s *PostgresStore) DeleteQRCode(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pay_qrcodes WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) GetQRCodeByPriceAndType(ctx context.Context, price float64, payType int) (*PayQRCode, error) {
	code := PayQRCode{}
	err := s.db.QueryRowContext(ctx, `SELECT id, pay_url, price, type FROM pay_qrcodes WHERE price = $1 AND type = $2 ORDER BY id DESC LIMIT 1`, round2(price), payType).Scan(&code.ID, &code.PayURL, &code.Price, &code.Type)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &code, nil
}

func (s *PostgresStore) ListQRCodes(ctx context.Context, page, limit int, typeFilter *int) ([]PayQRCode, int64, error) {
	where := ""
	args := []any{}
	next := 1
	if typeFilter != nil {
		where = fmt.Sprintf(" WHERE type = $%d", next)
		args = append(args, *typeFilter)
		next++
	}

	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pay_qrcodes`+where, args...).Scan(&count); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, (page-1)*limit)
	query := `SELECT id, pay_url, price, type FROM pay_qrcodes` + where + fmt.Sprintf(` ORDER BY id DESC LIMIT $%d OFFSET $%d`, next, next+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	codes := make([]PayQRCode, 0)
	for rows.Next() {
		code := PayQRCode{}
		if err := rows.Scan(&code.ID, &code.PayURL, &code.Price, &code.Type); err != nil {
			return nil, 0, err
		}
		codes = append(codes, code)
	}

	return codes, count, rows.Err()
}

func (s *PostgresStore) GetDashboardStats(ctx context.Context, start, end int64) (DashboardStats, error) {
	stats := DashboardStats{}
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pay_orders WHERE create_date >= $1 AND create_date <= $2`, start, end).Scan(&stats.TodayOrder)
	if err != nil {
		return stats, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(COUNT(*), 0) FROM pay_orders WHERE create_date >= $1 AND create_date <= $2 AND state IN (1, 2)`, start, end).Scan(&stats.TodaySuccessOrder); err != nil {
		return stats, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(COUNT(*), 0) FROM pay_orders WHERE create_date >= $1 AND create_date <= $2 AND state = -1`, start, end).Scan(&stats.TodayCloseOrder); err != nil {
		return stats, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(price), 0) FROM pay_orders WHERE create_date >= $1 AND create_date <= $2 AND state IN (1, 2)`, start, end).Scan(&stats.TodayMoney); err != nil {
		return stats, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(COUNT(*), 0) FROM pay_orders WHERE state = 1`).Scan(&stats.CountOrder); err != nil {
		return stats, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(price), 0) FROM pay_orders WHERE state IN (1, 2)`).Scan(&stats.CountMoney); err != nil {
		return stats, err
	}
	return stats, nil
}

func (s *PostgresStore) ExpireOrders(ctx context.Context, deadline, closeTime int64) ([]PayOrder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	rows, err := tx.QueryContext(ctx, `SELECT id, order_id, pay_id, create_date, pay_date, close_date, param, type, price, really_price, notify_url, return_url, state, is_auto, pay_url FROM pay_orders WHERE create_date < $1 AND state = 0 FOR UPDATE`, deadline)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	expired := make([]PayOrder, 0)
	for rows.Next() {
		order := PayOrder{}
		if err := rows.Scan(
			&order.ID,
			&order.OrderID,
			&order.PayID,
			&order.CreateDate,
			&order.PayDate,
			&order.CloseDate,
			&order.Param,
			&order.Type,
			&order.Price,
			&order.ReallyPrice,
			&order.NotifyURL,
			&order.ReturnURL,
			&order.State,
			&order.IsAuto,
			&order.PayURL,
		); err != nil {
			return nil, err
		}
		expired = append(expired, order)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range expired {
		expired[i].State = -1
		expired[i].CloseDate = closeTime
		if _, err := tx.ExecContext(ctx, `UPDATE pay_orders SET state = -1, close_date = $2 WHERE id = $1`, expired[i].ID, closeTime); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tmp_prices WHERE price = $1`, priceKey(expired[i].Type, expired[i].ReallyPrice)); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return expired, nil
}

type MemoryStore struct {
	mu           sync.RWMutex
	settings     map[string]string
	orders       map[int64]*PayOrder
	qrcodes      map[int64]*PayQRCode
	tmpPrices    map[string]struct{}
	orderSeq     int64
	qrcodeSeq    int64
	bootstrapped bool
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		settings:  make(map[string]string),
		orders:    make(map[int64]*PayOrder),
		qrcodes:   make(map[int64]*PayQRCode),
		tmpPrices: make(map[string]struct{}),
	}
}

func (m *MemoryStore) BootstrapDefaults(_ context.Context, now time.Time, cfg Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bootstrapped {
		return nil
	}
	defaults, err := defaultSettings(now, cfg)
	if err != nil {
		return err
	}
	for key, value := range defaults {
		m.settings[key] = value
	}
	m.bootstrapped = true
	return nil
}

func (m *MemoryStore) GetSettings(_ context.Context) (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string, len(m.settings))
	for key, value := range m.settings {
		out[key] = value
	}
	return out, nil
}

func (m *MemoryStore) GetSetting(_ context.Context, key string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	value, ok := m.settings[key]
	if !ok {
		return "", ErrNotFound
	}
	return value, nil
}

func (m *MemoryStore) UpsertSettings(_ context.Context, values map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, value := range values {
		m.settings[key] = value
	}
	return nil
}

func (m *MemoryStore) ReservePrice(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tmpPrices[key]; exists {
		return false, nil
	}
	m.tmpPrices[key] = struct{}{}
	return true, nil
}

func (m *MemoryStore) ReleasePrice(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tmpPrices, key)
	return nil
}

func (m *MemoryStore) CreateOrder(_ context.Context, order *PayOrder) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orderSeq++
	copyOrder := *order
	copyOrder.ID = m.orderSeq
	m.orders[copyOrder.ID] = &copyOrder
	order.ID = copyOrder.ID
	return nil
}

func (m *MemoryStore) UpdateOrder(_ context.Context, order *PayOrder) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copyOrder := *order
	m.orders[order.ID] = &copyOrder
	return nil
}

func (m *MemoryStore) GetOrderByPayID(_ context.Context, payID string) (*PayOrder, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, order := range m.orders {
		if order.PayID == payID {
			copyOrder := *order
			return &copyOrder, nil
		}
	}
	return nil, nil
}

func (m *MemoryStore) GetOrderByOrderID(_ context.Context, orderID string) (*PayOrder, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, order := range m.orders {
		if order.OrderID == orderID {
			copyOrder := *order
			return &copyOrder, nil
		}
	}
	return nil, nil
}

func (m *MemoryStore) GetOrderByID(_ context.Context, id int64) (*PayOrder, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	order, ok := m.orders[id]
	if !ok {
		return nil, nil
	}
	copyOrder := *order
	return &copyOrder, nil
}

func (m *MemoryStore) GetOrderByPayDate(_ context.Context, payDate int64) (*PayOrder, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, order := range m.orders {
		if order.PayDate == payDate {
			copyOrder := *order
			return &copyOrder, nil
		}
	}
	return nil, nil
}

func (m *MemoryStore) GetOpenOrderByPrice(_ context.Context, reallyPrice float64, payType int) (*PayOrder, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var best *PayOrder
	for _, order := range m.orders {
		if order.State == 0 && order.Type == payType && round2(order.ReallyPrice) == round2(reallyPrice) {
			if best == nil || order.ID > best.ID {
				copyOrder := *order
				best = &copyOrder
			}
		}
	}
	return best, nil
}

func (m *MemoryStore) MarkOrderPaidByPrice(_ context.Context, reallyPrice float64, payType int, payDate, closeDate int64) (*PayOrder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var chosen *PayOrder
	for _, order := range m.orders {
		if order.State == 0 && order.Type == payType && round2(order.ReallyPrice) == round2(reallyPrice) {
			if chosen == nil || order.ID > chosen.ID {
				chosen = order
			}
		}
	}
	if chosen == nil {
		return nil, nil
	}
	chosen.State = 1
	chosen.PayDate = payDate
	chosen.CloseDate = closeDate
	delete(m.tmpPrices, priceKey(chosen.Type, chosen.ReallyPrice))
	copyOrder := *chosen
	return &copyOrder, nil
}

func (m *MemoryStore) ListOrders(_ context.Context, page, limit int, filter OrderFilter) ([]PayOrder, int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]PayOrder, 0, len(m.orders))
	for _, order := range m.orders {
		if filter.Type != nil && order.Type != *filter.Type {
			continue
		}
		if filter.State != nil && order.State != *filter.State {
			continue
		}
		items = append(items, *order)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	total := int64(len(items))
	start := (page - 1) * limit
	if start >= len(items) {
		return []PayOrder{}, total, nil
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total, nil
}

func (m *MemoryStore) DeleteOrder(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.orders, id)
	return nil
}

func (m *MemoryStore) DeleteOrdersByState(_ context.Context, state int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, order := range m.orders {
		if order.State == state {
			delete(m.orders, id)
		}
	}
	return nil
}

func (m *MemoryStore) DeleteOrdersBeforeCreateDate(_ context.Context, before int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, order := range m.orders {
		if order.CreateDate < before {
			delete(m.orders, id)
		}
	}
	return nil
}

func (m *MemoryStore) CreateQRCode(_ context.Context, code *PayQRCode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.qrcodeSeq++
	copyCode := *code
	copyCode.ID = m.qrcodeSeq
	m.qrcodes[copyCode.ID] = &copyCode
	code.ID = copyCode.ID
	return nil
}

func (m *MemoryStore) DeleteQRCode(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.qrcodes, id)
	return nil
}

func (m *MemoryStore) GetQRCodeByPriceAndType(_ context.Context, price float64, payType int) (*PayQRCode, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var best *PayQRCode
	for _, code := range m.qrcodes {
		if code.Type == payType && round2(code.Price) == round2(price) {
			if best == nil || code.ID > best.ID {
				copyCode := *code
				best = &copyCode
			}
		}
	}
	return best, nil
}

func (m *MemoryStore) ListQRCodes(_ context.Context, page, limit int, typeFilter *int) ([]PayQRCode, int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]PayQRCode, 0, len(m.qrcodes))
	for _, code := range m.qrcodes {
		if typeFilter != nil && code.Type != *typeFilter {
			continue
		}
		items = append(items, *code)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	total := int64(len(items))
	start := (page - 1) * limit
	if start >= len(items) {
		return []PayQRCode{}, total, nil
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total, nil
}

func (m *MemoryStore) GetDashboardStats(_ context.Context, start, end int64) (DashboardStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	stats := DashboardStats{}
	for _, order := range m.orders {
		if order.CreateDate >= start && order.CreateDate <= end {
			stats.TodayOrder++
			if order.State == 1 || order.State == 2 {
				stats.TodaySuccessOrder++
				stats.TodayMoney += order.Price
			}
			if order.State == -1 {
				stats.TodayCloseOrder++
			}
		}
		if order.State == 1 {
			stats.CountOrder++
		}
		if order.State == 1 || order.State == 2 {
			stats.CountMoney += order.Price
		}
	}
	return stats, nil
}

func (m *MemoryStore) ExpireOrders(_ context.Context, deadline, closeTime int64) ([]PayOrder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	expired := make([]PayOrder, 0)
	for _, order := range m.orders {
		if order.State == 0 && order.CreateDate < deadline {
			order.State = -1
			order.CloseDate = closeTime
			delete(m.tmpPrices, priceKey(order.Type, order.ReallyPrice))
			expired = append(expired, *order)
		}
	}
	return expired, nil
}

func defaultSettings(now time.Time, cfg Config) (map[string]string, error) {
	adminUser := cfg.BootstrapAdminUser
	if adminUser == "" {
		adminUser = "admin"
	}
	adminPass := cfg.BootstrapAdminPass
	if adminPass == "" {
		adminPass = "admin"
	}
	hashedPass, err := hashAdminPassword(adminPass)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"user":      adminUser,
		"pass":      hashedPass,
		"notifyUrl": "",
		"returnUrl": "",
		"key":       newRandomHexSecret(32),
		"deviceKey": newRandomHexSecret(32),
		"lastheart": "0",
		"lastpay":   "0",
		"jkstate":   "-1",
		"close":     "5",
		"payQf":     "1",
		"wxpay":     "",
		"zfbpay":    "",
	}, nil
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func querySettingsTx(ctx context.Context, tx *sql.Tx) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT vkey, vvalue FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var key string
		var value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		settings[key] = value
	}
	return settings, rows.Err()
}
