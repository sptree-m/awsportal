package store

import (
	"context"
	"math/big"
	"strings"
)

type UserSettlement struct {
	RunID, State, Scope, Username, Amount, Source, Formula string
	UserID                                                 int64
}

func moneyMicros(raw string) string {
	n, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return "invalid"
	}
	sign := ""
	if n.Sign() < 0 {
		sign = "-"
	}
	digits := new(big.Int).Abs(n).String()
	if len(digits) < 7 {
		digits = strings.Repeat("0", 7-len(digits)) + digits
	}
	return sign + digits[:len(digits)-6] + "." + digits[len(digits)-6:]
}
func (s *Store) UserSettlements(ctx context.Context, u User) ([]UserSettlement, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT r.id,r.state,r.scope,u.username,a.micros,r.source_version,r.formula,u.id FROM billing_allocations a JOIN billing_runs r ON r.id=a.run_id JOIN users u ON u.id=a.user_id WHERE r.state IN ('PROVISIONAL','FINAL') AND (?='portal_admin' OR a.user_id=?) ORDER BY r.created_at DESC,u.id LIMIT 1000`, u.Role, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserSettlement
	for rows.Next() {
		var x UserSettlement
		var raw string
		if err = rows.Scan(&x.RunID, &x.State, &x.Scope, &x.Username, &raw, &x.Source, &x.Formula, &x.UserID); err != nil {
			return nil, err
		}
		x.Amount = moneyMicros(raw)
		out = append(out, x)
	}
	return out, rows.Err()
}
