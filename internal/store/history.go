package store

const historySchema = `
CREATE TABLE IF NOT EXISTS group_membership_history(id INTEGER PRIMARY KEY AUTOINCREMENT,user_id INTEGER NOT NULL,group_id INTEGER NOT NULL,valid_from INTEGER NOT NULL,valid_to INTEGER);
CREATE UNIQUE INDEX IF NOT EXISTS membership_current ON group_membership_history(user_id,group_id) WHERE valid_to IS NULL;
CREATE TABLE IF NOT EXISTS user_status_history(id INTEGER PRIMARY KEY AUTOINCREMENT,user_id INTEGER NOT NULL,enabled INTEGER NOT NULL,expires_at INTEGER NOT NULL,valid_from INTEGER NOT NULL,valid_to INTEGER);
CREATE UNIQUE INDEX IF NOT EXISTS user_status_current ON user_status_history(user_id) WHERE valid_to IS NULL;
INSERT INTO group_membership_history(user_id,group_id,valid_from) SELECT gm.user_id,gm.group_id,strftime('%s','now') FROM group_members gm WHERE NOT EXISTS(SELECT 1 FROM group_membership_history h WHERE h.user_id=gm.user_id AND h.group_id=gm.group_id AND h.valid_to IS NULL);
INSERT INTO user_status_history(user_id,enabled,expires_at,valid_from) SELECT u.id,u.enabled,u.expires_at,strftime('%s','now') FROM users u WHERE NOT EXISTS(SELECT 1 FROM user_status_history h WHERE h.user_id=u.id AND h.valid_to IS NULL);
CREATE TRIGGER IF NOT EXISTS membership_insert AFTER INSERT ON group_members BEGIN INSERT INTO group_membership_history(user_id,group_id,valid_from) VALUES(NEW.user_id,NEW.group_id,strftime('%s','now'));END;
CREATE TRIGGER IF NOT EXISTS membership_delete AFTER DELETE ON group_members BEGIN UPDATE group_membership_history SET valid_to=strftime('%s','now') WHERE user_id=OLD.user_id AND group_id=OLD.group_id AND valid_to IS NULL;END;
CREATE TRIGGER IF NOT EXISTS user_status_insert AFTER INSERT ON users BEGIN INSERT INTO user_status_history(user_id,enabled,expires_at,valid_from) VALUES(NEW.id,NEW.enabled,NEW.expires_at,strftime('%s','now'));END;
CREATE TRIGGER IF NOT EXISTS user_status_update AFTER UPDATE OF enabled,expires_at ON users WHEN NEW.enabled!=OLD.enabled OR NEW.expires_at!=OLD.expires_at BEGIN UPDATE user_status_history SET valid_to=strftime('%s','now') WHERE user_id=OLD.id AND valid_to IS NULL;INSERT INTO user_status_history(user_id,enabled,expires_at,valid_from) VALUES(NEW.id,NEW.enabled,NEW.expires_at,strftime('%s','now'));END;
`
