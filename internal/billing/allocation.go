// Package billing implements immutable CUR settlement, independently of the
// Cost Explorer estimate shown on the legacy dashboard.
package billing

import (
	"encoding/csv"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"
)

const FormulaVersion = "dcv-time-largest-remainder-micros-v2"

var decimalAmount = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?$`)

type Line struct {
	ID, Account, Currency, Type, Resource, Amount string
	Start, End                                    time.Time
}
type Scope struct {
	Account, Currency, Basis string
	Start, End               time.Time
}
type Weight struct {
	UserID int64
	Units  int64
}
type Allocation struct {
	UserID int64
	Micros *big.Int
}

// Money has an explicit six-decimal settlement precision. The original CUR
// decimal remains in immutable source rows; rounding occurs once at run total.
func Micros(decimal string) (*big.Int, error) {
	r, ok := new(big.Rat).SetString(decimal)
	if !ok {
		return nil, fmt.Errorf("invalid decimal")
	}
	r.Mul(r, big.NewRat(1000000, 1))
	n := new(big.Int).Set(r.Num())
	sign := n.Sign()
	n.Abs(n)
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(n, r.Denom(), rem)
	if new(big.Int).Mul(rem, big.NewInt(2)).Cmp(r.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if sign < 0 {
		q.Neg(q)
	}
	return q, nil
}

// Signed largest remainder: credits and refunds are symmetric with costs.
// Equal remainders use immutable user IDs, never map iteration order.
func Split(total *big.Int, weights []Weight) ([]Allocation, error) {
	var sum int64
	seen := map[int64]bool{}
	for _, w := range weights {
		if w.UserID <= 0 || w.Units < 0 || seen[w.UserID] {
			return nil, fmt.Errorf("invalid weight")
		}
		seen[w.UserID] = true
		if w.Units > int64(^uint64(0)>>1)-sum {
			return nil, fmt.Errorf("weight overflow")
		}
		sum += w.Units
	}
	if sum == 0 {
		return nil, fmt.Errorf("no allocation weights")
	}
	type share struct {
		Allocation
		remainder *big.Int
	}
	var shares []share
	used := new(big.Int)
	magnitude := new(big.Int).Abs(total)
	for _, w := range weights {
		if w.Units == 0 {
			continue
		}
		n := new(big.Int).Mul(magnitude, big.NewInt(w.Units))
		q, r := new(big.Int), new(big.Int)
		q.QuoRem(n, big.NewInt(sum), r)
		shares = append(shares, share{Allocation{w.UserID, q}, r})
		used.Add(used, q)
	}
	sort.Slice(shares, func(i, j int) bool {
		c := shares[i].remainder.Cmp(shares[j].remainder)
		if c == 0 {
			return shares[i].UserID < shares[j].UserID
		}
		return c > 0
	})
	left := new(big.Int).Sub(magnitude, used).Int64()
	for i := int64(0); i < left; i++ {
		shares[i].Micros.Add(shares[i].Micros, big.NewInt(1))
	}
	out := make([]Allocation, len(shares))
	for i, s := range shares {
		if total.Sign() < 0 {
			s.Micros.Neg(s.Micros)
		}
		out[i] = s.Allocation
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

// ReadCUR streams gzip-decompressed CUR 2.0 CSV. No whole-month in-memory load.
// Header names are normalized across the slash and underscore CUR conventions.
func ReadCUR(r io.Reader, scope Scope, emit func(Line) error) error {
	if scope.Account == "" || scope.Currency == "" || !scope.End.After(scope.Start) {
		return fmt.Errorf("account currency period required")
	}
	basis := map[string]string{"unblended": "line_item_unblended_cost", "net_unblended": "line_item_net_unblended_cost"}[scope.Basis]
	if basis == "" {
		return fmt.Errorf("explicit supported cost basis required")
	}
	c := csv.NewReader(r)
	header, err := c.Read()
	if err != nil {
		return err
	}
	indices := map[string]int{}
	for i, h := range header {
		indices[strings.ToLower(strings.ReplaceAll(h, "/", "_"))] = i
	}
	required := []string{"identity_line_item_id", "line_item_usage_account_id", "line_item_currency_code", "line_item_line_item_type", "line_item_resource_id", "line_item_usage_start_date", "line_item_usage_end_date", basis}
	for _, h := range required {
		if _, ok := indices[h]; !ok {
			return fmt.Errorf("missing CUR column %s", h)
		}
	}
	for {
		row, err := c.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		get := func(k string) string { return row[indices[k]] }
		start, err := time.Parse(time.RFC3339, get("line_item_usage_start_date"))
		if err != nil {
			return err
		}
		end, err := time.Parse(time.RFC3339, get("line_item_usage_end_date"))
		if err != nil {
			return err
		}
		if get("line_item_usage_account_id") != scope.Account || get("line_item_currency_code") != scope.Currency || start.Before(scope.Start) || !start.Before(scope.End) || end.After(scope.End) {
			continue
		}
		amount := get(basis)
		if amount == "" && scope.Basis == "net_unblended" {
			if i, ok := indices["line_item_unblended_cost"]; ok {
				amount = row[i]
			}
		}
		if len(amount) > 128 || !decimalAmount.MatchString(amount) {
			return fmt.Errorf("invalid decimal cost for selected basis")
		}
		if _, ok := new(big.Rat).SetString(amount); !ok {
			return fmt.Errorf("invalid CUR amount")
		}
		l := Line{get("identity_line_item_id"), scope.Account, scope.Currency, get("line_item_line_item_type"), get("line_item_resource_id"), amount, start, end}
		if l.ID == "" {
			return fmt.Errorf("line identity required")
		}
		if err = emit(l); err != nil {
			return err
		}
	}
}
