package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type Group struct {
	ID   int64
	Name string
}
type GroupMember struct {
	GroupID, UserID     int64
	GroupName, Username string
}
type Assignment struct {
	Kind, Name string
	SubjectID  int64
	CanControl bool
}
type ManagedInstance struct {
	Instance
	Enabled     bool
	Assignments []Assignment
}

func (s *Store) ManagedInstances(ctx context.Context) ([]ManagedInstance, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT id,instance_id,name,dcv_host,dcv_session_id,enabled FROM instances ORDER BY name")
	if e != nil {
		return nil, e
	}
	var out []ManagedInstance
	for rows.Next() {
		var x ManagedInstance
		if e = rows.Scan(&x.ID, &x.InstanceID, &x.Name, &x.DCVHost, &x.DCVSessionID, &x.Enabled); e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for i := range out {
		rs, err := s.DB.QueryContext(ctx, `SELECT 'user',u.id,u.username,iu.can_control FROM instance_users iu JOIN users u ON u.id=iu.user_id WHERE iu.instance_id=? UNION ALL SELECT 'group',g.id,g.name,ig.can_control FROM instance_groups ig JOIN groups g ON g.id=ig.group_id WHERE ig.instance_id=? ORDER BY 1,3`, out[i].ID, out[i].ID)
		if err != nil {
			return nil, err
		}
		for rs.Next() {
			var a Assignment
			if err = rs.Scan(&a.Kind, &a.SubjectID, &a.Name, &a.CanControl); err != nil {
				rs.Close()
				return nil, err
			}
			out[i].Assignments = append(out[i].Assignments, a)
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	rs, e := s.DB.QueryContext(ctx, "SELECT id,name FROM groups ORDER BY name")
	if e != nil {
		return nil, e
	}
	defer rs.Close()
	var out []Group
	for rs.Next() {
		var g Group
		if e = rs.Scan(&g.ID, &g.Name); e != nil {
			return nil, e
		}
		out = append(out, g)
	}
	return out, rs.Err()
}
func (s *Store) GroupMembers(ctx context.Context) ([]GroupMember, error) {
	rs, e := s.DB.QueryContext(ctx, `SELECT g.id,u.id,g.name,u.username FROM group_members gm JOIN groups g ON g.id=gm.group_id JOIN users u ON u.id=gm.user_id ORDER BY g.name,u.username`)
	if e != nil {
		return nil, e
	}
	defer rs.Close()
	var out []GroupMember
	for rs.Next() {
		var m GroupMember
		if e = rs.Scan(&m.GroupID, &m.UserID, &m.GroupName, &m.Username); e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rs.Err()
}
func adminOnly(u User) error {
	if u.Role != "portal_admin" {
		return fmt.Errorf("portal administrator required")
	}
	return nil
}
func (s *Store) SetInstanceEnabled(ctx context.Context, u User, id string, enabled bool) error {
	if e := adminOnly(u); e != nil {
		return e
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, "UPDATE instances SET enabled=? WHERE instance_id=?", enabled, id)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	if !enabled {
		if _, e = tx.ExecContext(ctx, "DELETE FROM dcv_tokens WHERE instance_id=(SELECT id FROM instances WHERE instance_id=?)", id); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) SetAssignment(ctx context.Context, u User, id, kind string, subjectID int64, control, remove bool) error {
	if e := adminOnly(u); e != nil {
		return e
	}
	table, column := "instance_users", "user_id"
	if kind == "group" {
		table, column = "instance_groups", "group_id"
	} else if kind != "user" {
		return fmt.Errorf("invalid subject kind")
	}
	var iid int64
	if e := s.DB.QueryRowContext(ctx, "SELECT id FROM instances WHERE instance_id=?", id).Scan(&iid); e != nil {
		return e
	}
	if remove {
		_, e := s.DB.ExecContext(ctx, "DELETE FROM "+table+" WHERE instance_id=? AND "+column+"=?", iid, subjectID)
		return e
	}
	_, e := s.DB.ExecContext(ctx, "INSERT INTO "+table+"(instance_id,"+column+",can_control) VALUES(?,?,?) ON CONFLICT(instance_id,"+column+") DO UPDATE SET can_control=excluded.can_control", iid, subjectID, control)
	return e
}
func (s *Store) CreateGroup(ctx context.Context, u User, name string) error {
	if e := adminOnly(u); e != nil {
		return e
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 64 {
		return fmt.Errorf("invalid group name")
	}
	_, e := s.DB.ExecContext(ctx, "INSERT INTO groups(name) VALUES(?)", name)
	return e
}
func (s *Store) SetGroupMember(ctx context.Context, u User, gid, uid int64, remove bool) error {
	if e := adminOnly(u); e != nil {
		return e
	}
	if remove {
		_, e := s.DB.ExecContext(ctx, "DELETE FROM group_members WHERE group_id=? AND user_id=?", gid, uid)
		return e
	}
	_, e := s.DB.ExecContext(ctx, "INSERT INTO group_members(group_id,user_id) VALUES(?,?) ON CONFLICT DO NOTHING", gid, uid)
	return e
}
