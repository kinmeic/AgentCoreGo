package store

import (
	"agent-core/internal/model"
	"time"
)

func (s *SQLite) SaveHostCallback(callback *model.HostCallback) error {
	now := time.Now()
	if callback.CreatedAt.IsZero() {
		callback.CreatedAt = now
	}
	callback.UpdatedAt = now
	_, err := s.db.Exec(`INSERT INTO host_callbacks(
		name, description, schema, callback_url, secret, allow_private, enabled, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(name) DO UPDATE SET
		description=excluded.description, schema=excluded.schema, callback_url=excluded.callback_url,
		secret=excluded.secret, allow_private=excluded.allow_private, enabled=excluded.enabled,
		updated_at=excluded.updated_at`, callback.Name, callback.Description, string(callback.Schema),
		callback.CallbackURL, callback.Secret, callback.AllowPrivate, callback.Enabled, callback.CreatedAt, callback.UpdatedAt)
	return err
}

func (s *SQLite) HostCallbacks() ([]*model.HostCallback, error) {
	rows, err := s.db.Query(`SELECT name, description, schema, callback_url, secret,
		allow_private, enabled, created_at, updated_at FROM host_callbacks ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var callbacks []*model.HostCallback
	for rows.Next() {
		var callback model.HostCallback
		var schema string
		if err := rows.Scan(&callback.Name, &callback.Description, &schema,
			&callback.CallbackURL, &callback.Secret, &callback.AllowPrivate, &callback.Enabled,
			&callback.CreatedAt, &callback.UpdatedAt); err != nil {
			return nil, err
		}
		callback.Schema = []byte(schema)
		callbacks = append(callbacks, &callback)
	}
	return callbacks, rows.Err()
}

func (s *SQLite) DeleteHostCallback(name string) error {
	_, err := s.db.Exec(`DELETE FROM host_callbacks WHERE name = ?`, name)
	return err
}
