package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/sptree-m/awsportal/internal/billing"
	"io"
	"math/big"
	"sort"
	"time"
)

const billingSchema = `
CREATE TABLE IF NOT EXISTS billing_runs(id TEXT PRIMARY KEY,source_version TEXT NOT NULL,scope TEXT NOT NULL,policy TEXT NOT NULL,formula TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'PROVISIONAL',actual_micros TEXT NOT NULL DEFAULT '0',residual_micros TEXT NOT NULL DEFAULT '0',created_at INTEGER NOT NULL,finalized_at INTEGER NOT NULL DEFAULT 0,UNIQUE(source_version,scope,policy));
CREATE TABLE IF NOT EXISTS billing_lines(run_id TEXT NOT NULL REFERENCES billing_runs(id),line_id TEXT NOT NULL,resource_id TEXT NOT NULL,line_type TEXT NOT NULL,amount TEXT NOT NULL,start_at INTEGER NOT NULL,end_at INTEGER NOT NULL,reason TEXT NOT NULL DEFAULT '',PRIMARY KEY(run_id,line_id));
CREATE TABLE IF NOT EXISTS billing_line_weights(run_id TEXT NOT NULL,line_id TEXT NOT NULL,user_id INTEGER NOT NULL,units INTEGER NOT NULL,PRIMARY KEY(run_id,line_id,user_id));
CREATE TABLE IF NOT EXISTS billing_allocations(run_id TEXT NOT NULL REFERENCES billing_runs(id),user_id INTEGER NOT NULL REFERENCES users(id),micros TEXT NOT NULL,PRIMARY KEY(run_id,user_id));
`

type BillingPolicy struct {
	NonResource    map[string]string `json:"non_resource"`
	CommonGroupID  int64             `json:"common_group_id"`
	FallbackUserID int64             `json:"fallback_user_id"`
}
type BillingRun struct {
	ID, SourceVersion, Scope, Policy, Formula, State, ActualMicros, ResidualMicros string
	CreatedAt, FinalizedAt                                                         int64
}

// SourceVersion is the immutable CUR manifest/object version digest; corrected
// exports create a new run. An existing source cannot be silently overwritten.
func (s *Store) ImportBilling(ctx context.Context, admin User, source string, scope billing.Scope, policy BillingPolicy, r io.Reader) (string, error) {
	if err := adminOnly(admin); err != nil {
		return "", err
	}
	if source == "" || policy.FallbackUserID < 0 || policy.CommonGroupID < 0 {
		return "", fmt.Errorf("immutable source version and explicit policy required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()
	id, err := operationID()
	if err != nil {
		return "", err
	}
	sj, _ := json.Marshal(scope)
	pj, _ := json.Marshal(policy)
	var existing, existingState string
	err = tx.QueryRowContext(ctx, `SELECT id,state FROM billing_runs WHERE source_version=? AND scope=? AND policy=?`, source, string(sj), string(pj)).Scan(&existing, &existingState)
	if err == nil && (existingState == "PROVISIONAL" || existingState == "FINAL") {
		return existing, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if existing != "" {
		id = existing
		for _, table := range []string{"billing_line_weights", "billing_lines", "billing_allocations"} {
			if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE run_id=?`, id); err != nil {
				return "", err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE billing_runs SET state='IMPORTING' WHERE id=?`, id); err != nil {
			return "", err
		}
	} else if _, err = tx.ExecContext(ctx, `INSERT INTO billing_runs(id,source_version,scope,policy,formula,state,created_at) VALUES(?,?,?,?,?,'IMPORTING',?)`, id, source, string(sj), string(pj), billing.FormulaVersion, time.Now().Unix()); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	tx = nil

	amounts := map[int64]*big.Rat{}
	actual := new(big.Rat)
	add := func(uid int64, a *big.Rat) {
		if amounts[uid] == nil {
			amounts[uid] = new(big.Rat)
		}
		amounts[uid].Add(amounts[uid], a)
	}
	lineCount := 0
	err = billing.ReadCUR(r, scope, func(l billing.Line) error {
		lineTx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer lineTx.Rollback()
		tx = lineTx
		lineCount++
		cost, _ := new(big.Rat).SetString(l.Amount)
		actual.Add(actual, cost)
		weights, reason, err := billingWeights(ctx, tx, l, scope, policy)
		if err != nil {
			return err
		}
		for _, w := range weights {
			if _, err = tx.ExecContext(ctx, `INSERT INTO billing_line_weights VALUES(?,?,?,?)`, id, l.ID, w.UserID, w.Units); err != nil {
				return err
			}
		}
		if len(weights) == 0 {
			add(0, cost)
		} else {
			var sum int64
			for _, w := range weights {
				sum += w.Units
			}
			if sum == 0 {
				add(0, cost)
			} else {
				for _, w := range weights {
					add(w.UserID, new(big.Rat).Mul(cost, big.NewRat(w.Units, sum)))
				}
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO billing_lines(run_id,line_id,resource_id,line_type,amount,start_at,end_at,reason) VALUES(?,?,?,?,?,?,?,?)`, id, l.ID, l.Resource, l.Type, l.Amount, l.Start.Unix(), l.End.Unix(), reason)
		if err != nil {
			return err
		}
		if err = lineTx.Commit(); err != nil {
			return err
		}
		tx = nil
		return nil
	})
	if err != nil || lineCount == 0 {
		if tx != nil {
			tx.Rollback()
			tx = nil
		}
		s.DB.ExecContext(ctx, `UPDATE billing_runs SET state='FAILED' WHERE id=?`, id)
		if err == nil {
			err = fmt.Errorf("no rows in selected account/currency/period")
		}
		return "", err
	}
	tx, err = s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	totals := settleRationals(amounts, actual)
	residual := "0"
	if x := totals[0]; x != nil {
		residual = x.String()
	}
	total, _ := billing.Micros(actual.FloatString(30))
	for uid, m := range totals {
		if uid == 0 {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO billing_allocations VALUES(?,?,?)`, id, uid, m.String()); err != nil {
			return "", err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE billing_runs SET state='PROVISIONAL',actual_micros=?,residual_micros=? WHERE id=?`, total.String(), residual, id); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

// Preserve exact decimals throughout weighting. Floor signed shares and award
// the largest fractional remainders once, after the whole run is accumulated.
func settleRationals(amounts map[int64]*big.Rat, actual *big.Rat) map[int64]*big.Int {
	type fraction struct {
		uid int64
		rem *big.Rat
	}
	var fs []fraction
	out := map[int64]*big.Int{}
	sum := new(big.Int)
	for uid, a := range amounts {
		scaled := new(big.Rat).Mul(a, big.NewRat(1000000, 1))
		q, r := new(big.Int), new(big.Int)
		q.QuoRem(scaled.Num(), scaled.Denom(), r)
		if r.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
			r.Add(r, scaled.Denom())
		}
		out[uid] = q
		sum.Add(sum, q)
		fs = append(fs, fraction{uid, new(big.Rat).SetFrac(r, scaled.Denom())})
	}
	sort.Slice(fs, func(i, j int) bool {
		c := fs[i].rem.Cmp(fs[j].rem)
		if c == 0 {
			return fs[i].uid < fs[j].uid
		}
		return c > 0
	})
	total, _ := billing.Micros(actual.FloatString(30))
	left := new(big.Int).Sub(total, sum).Int64()
	for i := int64(0); i < left && i < int64(len(fs)); i++ {
		out[fs[i].uid].Add(out[fs[i].uid], big.NewInt(1))
	}
	return out
}
func billingWeights(ctx context.Context, tx *sql.Tx, l billing.Line, scope billing.Scope, p BillingPolicy) ([]billing.Weight, string, error) {
	var owner, group, eid int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(owner_user_id,0),COALESCE(group_id,0),COALESCE(environment_id,0) FROM resource_registry WHERE resource_id=? AND valid_from<=? AND (valid_to IS NULL OR valid_to>=?) ORDER BY valid_from DESC LIMIT 1`, l.Resource, l.Start.Unix(), l.End.Unix()).Scan(&owner, &group, &eid)
	if err != nil && err != sql.ErrNoRows {
		return nil, "", err
	}
	if err == nil && owner > 0 {
		return []billing.Weight{{UserID: owner, Units: 1}}, "personal owner 100%", nil
	}
	if l.Resource == "" || err == sql.ErrNoRows {
		rule := p.NonResource[l.Type]
		if rule == "fallback_owner" && p.FallbackUserID > 0 {
			return []billing.Weight{{UserID: p.FallbackUserID, Units: 1}}, "explicit non-resource policy: " + l.Type, nil
		}
		if rule == "group" && p.CommonGroupID > 0 {
			return commonGroupWeights(ctx, tx, p.CommonGroupID, scope, p)
		}
		return nil, "unmapped resource or non-resource line type", nil
	}
	// Actual connected intervals require consecutive samples from the same boot,
	// generation, <=90 seconds apart, with positive DCV connections at both ends.
	start, end := l.Start.Unix(), l.End.Unix()
	resource := l.Resource
	var instanceResource string
	if err = tx.QueryRowContext(ctx, `SELECT i.instance_id FROM instance_resources r JOIN instances i ON i.id=r.instance_id WHERE r.resource_id=?`, resource).Scan(&instanceResource); err != nil && err != sql.ErrNoRows {
		return nil, "", err
	}
	if instanceResource != "" {
		resource = instanceResource
	}
	if group > 0 && eid == 0 {
		start, end = scope.Start.Unix(), scope.End.Unix()
		resource = ""
	}
	rows, err := tx.QueryContext(ctx, `SELECT ci.user_id,SUM(MIN(ci.end_at,?)-MAX(ci.start_at,?)) FROM connection_intervals ci JOIN instances i ON i.id=ci.instance_id WHERE ci.end_at>? AND ci.start_at<? AND ((?!='' AND i.instance_id=?) OR (?='' AND ci.group_id=?)) GROUP BY ci.user_id`, end, start, start, end, resource, resource, resource, group)
	if err != nil {
		return nil, "", err
	}
	byUser := map[int64]int64{}
	for rows.Next() {
		var uid, n int64
		if err = rows.Scan(&uid, &n); err != nil {
			rows.Close()
			return nil, "", err
		}
		byUser[uid] = n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	var weights []billing.Weight
	for uid, n := range byUser {
		weights = append(weights, billing.Weight{UserID: uid, Units: n})
	}
	if len(weights) > 0 {
		return weights, "measured DCV connected seconds", nil
	}
	if group > 0 {
		return commonGroupWeights(ctx, tx, group, scope, p)
	}

	if p.FallbackUserID > 0 {
		return []billing.Weight{{UserID: p.FallbackUserID, Units: 1}}, "explicit empty-group fallback owner", nil
	}
	return nil, "unallocated: no exposure, members or fallback", nil
}
func commonGroupWeights(ctx context.Context, tx *sql.Tx, group int64, scope billing.Scope, p BillingPolicy) ([]billing.Weight, string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT user_id,SUM(MIN(end_at,?)-MAX(start_at,?)) FROM connection_intervals WHERE group_id=? AND end_at>? AND start_at<? GROUP BY user_id`, scope.End.Unix(), scope.Start.Unix(), group, scope.Start.Unix(), scope.End.Unix())
	if err != nil {
		return nil, "", err
	}
	var weights []billing.Weight
	for rows.Next() {
		var w billing.Weight
		if err = rows.Scan(&w.UserID, &w.Units); err != nil {
			rows.Close()
			return nil, "", err
		}
		if w.Units > 0 {
			weights = append(weights, w)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	if len(weights) > 0 {
		return weights, "group monthly measured DCV connected seconds", nil
	}
	rows, err = tx.QueryContext(ctx, `SELECT DISTINCT h.user_id FROM group_membership_history h JOIN user_status_history us ON us.user_id=h.user_id WHERE h.group_id=? AND us.enabled=1 AND MAX(h.valid_from,us.valid_from,?)<MIN(COALESCE(h.valid_to,?),COALESCE(us.valid_to,?),CASE WHEN us.expires_at=0 THEN ? ELSE us.expires_at END,?) ORDER BY h.user_id`, group, scope.Start.Unix(), scope.End.Unix(), scope.End.Unix(), scope.End.Unix(), scope.End.Unix())
	if err != nil {
		return nil, "", err
	}
	for rows.Next() {
		var uid int64
		if err = rows.Scan(&uid); err != nil {
			rows.Close()
			return nil, "", err
		}
		weights = append(weights, billing.Weight{UserID: uid, Units: 1})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	if len(weights) > 0 {
		return weights, "active group members equally; no measured connection", nil
	}
	if p.FallbackUserID > 0 {
		return []billing.Weight{{UserID: p.FallbackUserID, Units: 1}}, "explicit empty-group fallback owner", nil
	}
	return nil, "unallocated: no group exposure, members or fallback", nil
}
func (s *Store) FinalizeBilling(ctx context.Context, admin User, id string) error {
	if err := adminOnly(admin); err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE billing_runs SET state='FINAL',finalized_at=? WHERE id=? AND state='PROVISIONAL' AND residual_micros='0'`, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("only reconciled provisional runs can be finalized")
	}
	return nil
}
func (s *Store) BillingRuns(ctx context.Context) ([]BillingRun, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,source_version,scope,policy,formula,state,actual_micros,residual_micros,created_at,finalized_at FROM billing_runs ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var xs []BillingRun
	for rows.Next() {
		var x BillingRun
		if err = rows.Scan(&x.ID, &x.SourceVersion, &x.Scope, &x.Policy, &x.Formula, &x.State, &x.ActualMicros, &x.ResidualMicros, &x.CreatedAt, &x.FinalizedAt); err != nil {
			return nil, err
		}
		xs = append(xs, x)
	}
	return xs, rows.Err()
}
