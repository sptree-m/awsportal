package store

import (
 "context"
 "database/sql"
 _ "modernc.org/sqlite"
)
type Store struct{ DB *sql.DB }
func Open(path string)(*Store,error){ db,e:=sql.Open("sqlite",path); if e!=nil{return nil,e}; return &Store{DB:db},nil }
func(s *Store)Close()error{return s.DB.Close()}
func(s *Store)Migrate(ctx context.Context)error{ _,e:=s.DB.ExecContext(ctx, schema); return e }
const schema = `PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY, username TEXT UNIQUE NOT NULL, password_hash TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('user','group_admin','portal_admin')), totp_secret_enc BLOB, enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS groups(id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL);
CREATE TABLE IF NOT EXISTS group_members(user_id INTEGER NOT NULL REFERENCES users(id), group_id INTEGER NOT NULL REFERENCES groups(id), PRIMARY KEY(user_id,group_id));
CREATE TABLE IF NOT EXISTS instances(id INTEGER PRIMARY KEY, instance_id TEXT UNIQUE NOT NULL, name TEXT NOT NULL, dcv_host TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS instance_users(instance_id INTEGER NOT NULL REFERENCES instances(id), user_id INTEGER NOT NULL REFERENCES users(id), can_control INTEGER NOT NULL DEFAULT 1, PRIMARY KEY(instance_id,user_id));
CREATE TABLE IF NOT EXISTS instance_groups(instance_id INTEGER NOT NULL REFERENCES instances(id), group_id INTEGER NOT NULL REFERENCES groups(id), can_control INTEGER NOT NULL DEFAULT 1, PRIMARY KEY(instance_id,group_id));
CREATE TABLE IF NOT EXISTS schedules(id INTEGER PRIMARY KEY, instance_id INTEGER NOT NULL REFERENCES instances(id), owner_user_id INTEGER NOT NULL REFERENCES users(id), action TEXT NOT NULL CHECK(action IN ('start','stop')), cron_expr TEXT NOT NULL, timezone TEXT NOT NULL DEFAULT 'Asia/Tokyo', enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS audit_log(id INTEGER PRIMARY KEY, at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, actor TEXT NOT NULL, action TEXT NOT NULL, target TEXT, result TEXT NOT NULL, detail TEXT);
`
