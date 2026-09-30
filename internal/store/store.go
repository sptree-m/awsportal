package store

import (
 "context"
 "database/sql"
 "time"
 _ "modernc.org/sqlite"
)

type Store struct{ DB *sql.DB }
type User struct{ ID int64; Username, PasswordHash, Role, TOTPSecret string; Enabled bool }
type Instance struct{ ID int64; InstanceID, Name, DCVHost string; CanControl bool }
type Schedule struct{ ID, InstanceDBID, OwnerUserID int64; Action, TimeHHMM, Weekdays, Timezone string; Enabled bool }

func Open(path string)(*Store,error){ db,e:=sql.Open("sqlite",path); if e!=nil{return nil,e}; db.SetMaxOpenConns(1); return &Store{DB:db},nil }
func(s *Store)Close()error{return s.DB.Close()}
func(s *Store)Migrate(ctx context.Context)error{ _,e:=s.DB.ExecContext(ctx,schema); return e }

func(s *Store)UserByName(ctx context.Context,n string)(User,error){var u User;var en int;err:=s.DB.QueryRowContext(ctx,"SELECT id,username,password_hash,role,COALESCE(totp_secret,''),enabled FROM users WHERE username=?",n).Scan(&u.ID,&u.Username,&u.PasswordHash,&u.Role,&u.TOTPSecret,&en);u.Enabled=en==1;return u,err}
func(s *Store)CreateUser(ctx context.Context,n,h,role,totp string)error{_,e:=s.DB.ExecContext(ctx,"INSERT INTO users(username,password_hash,role,totp_secret,enabled) VALUES(?,?,?,?,1)",n,h,role,totp);return e}
func(s *Store)VisibleInstances(ctx context.Context,u User)([]Instance,error){
 q:=`SELECT DISTINCT i.id,i.instance_id,i.name,i.dcv_host,
 CASE WHEN ?='portal_admin' OR COALESCE(iu.can_control,0)=1 OR COALESCE(ig.can_control,0)=1 THEN 1 ELSE 0 END
 FROM instances i
 LEFT JOIN instance_users iu ON iu.instance_id=i.id AND iu.user_id=?
 LEFT JOIN group_members gm ON gm.user_id=?
 LEFT JOIN instance_groups ig ON ig.instance_id=i.id AND ig.group_id=gm.group_id
 WHERE i.enabled=1 AND (?='portal_admin' OR iu.user_id IS NOT NULL OR ig.group_id IS NOT NULL)
 ORDER BY i.name`
 rows,e:=s.DB.QueryContext(ctx,q,u.Role,u.ID,u.ID,u.Role);if e!=nil{return nil,e};defer rows.Close()
 var out []Instance;for rows.Next(){var x Instance;var c int;if e=rows.Scan(&x.ID,&x.InstanceID,&x.Name,&x.DCVHost,&c);e!=nil{return nil,e};x.CanControl=c==1;out=append(out,x)};return out,rows.Err()
}
func(s *Store)CanControl(ctx context.Context,u User,awsID string)bool{if u.Role=="portal_admin"{return true};var n int;_ = s.DB.QueryRowContext(ctx,`SELECT COUNT(*) FROM instances i LEFT JOIN instance_users iu ON iu.instance_id=i.id AND iu.user_id=? LEFT JOIN group_members gm ON gm.user_id=? LEFT JOIN instance_groups ig ON ig.instance_id=i.id AND ig.group_id=gm.group_id WHERE i.instance_id=? AND (iu.can_control=1 OR ig.can_control=1)`,u.ID,u.ID,awsID).Scan(&n);return n>0}
func(s *Store)AddSchedule(ctx context.Context,u User,awsID,action,hhmm,weekdays,tz string)error{var iid int64;if e:=s.DB.QueryRowContext(ctx,"SELECT id FROM instances WHERE instance_id=?",awsID).Scan(&iid);e!=nil{return e};_,e:=s.DB.ExecContext(ctx,"INSERT INTO schedules(instance_id,owner_user_id,action,time_hhmm,weekdays,timezone,enabled) VALUES(?,?,?,?,?,?,1)",iid,u.ID,action,hhmm,weekdays,tz);return e}
func(s *Store)DueSchedules(ctx context.Context,t time.Time)([]struct{Schedule;InstanceID string},error){rows,e:=s.DB.QueryContext(ctx,`SELECT s.id,s.instance_id,s.owner_user_id,s.action,s.time_hhmm,s.weekdays,s.timezone,s.enabled,i.instance_id FROM schedules s JOIN instances i ON i.id=s.instance_id WHERE s.enabled=1`);if e!=nil{return nil,e};defer rows.Close();var out []struct{Schedule;InstanceID string};for rows.Next(){var x struct{Schedule;InstanceID string};var en int;if e=rows.Scan(&x.ID,&x.InstanceDBID,&x.OwnerUserID,&x.Action,&x.TimeHHMM,&x.Weekdays,&x.Timezone,&en,&x.InstanceID);e!=nil{return nil,e};x.Enabled=en==1;out=append(out,x)};return out,rows.Err()}
func(s *Store)Audit(ctx context.Context,actor,action,target,result,detail string){_,_=s.DB.ExecContext(ctx,"INSERT INTO audit_log(actor,action,target,result,detail) VALUES(?,?,?,?,?)",actor,action,target,result,detail)}

const schema=`PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY,username TEXT UNIQUE NOT NULL,password_hash TEXT NOT NULL,role TEXT NOT NULL CHECK(role IN ('user','group_admin','portal_admin')),totp_secret TEXT,enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS groups(id INTEGER PRIMARY KEY,name TEXT UNIQUE NOT NULL);
CREATE TABLE IF NOT EXISTS group_members(user_id INTEGER NOT NULL REFERENCES users(id),group_id INTEGER NOT NULL REFERENCES groups(id),PRIMARY KEY(user_id,group_id));
CREATE TABLE IF NOT EXISTS instances(id INTEGER PRIMARY KEY,instance_id TEXT UNIQUE NOT NULL,name TEXT NOT NULL,dcv_host TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS instance_users(instance_id INTEGER NOT NULL REFERENCES instances(id),user_id INTEGER NOT NULL REFERENCES users(id),can_control INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(instance_id,user_id));
CREATE TABLE IF NOT EXISTS instance_groups(instance_id INTEGER NOT NULL REFERENCES instances(id),group_id INTEGER NOT NULL REFERENCES groups(id),can_control INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(instance_id,group_id));
CREATE TABLE IF NOT EXISTS schedules(id INTEGER PRIMARY KEY,instance_id INTEGER NOT NULL REFERENCES instances(id),owner_user_id INTEGER NOT NULL REFERENCES users(id),action TEXT NOT NULL CHECK(action IN ('start','stop')),time_hhmm TEXT NOT NULL,weekdays TEXT NOT NULL DEFAULT '1,2,3,4,5',timezone TEXT NOT NULL DEFAULT 'Asia/Tokyo',enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS audit_log(id INTEGER PRIMARY KEY,at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,actor TEXT NOT NULL,action TEXT NOT NULL,target TEXT,result TEXT NOT NULL,detail TEXT);`
