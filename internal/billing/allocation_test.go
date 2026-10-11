package billing

import (
	"math/big"
	"testing"
)

func TestSignedReconciliation(t *testing.T) {
	for _, n := range []int64{0, 1, 2, 1000001, -1, -1000001} {
		xs, err := Split(big.NewInt(n), []Weight{{3, 1}, {1, 1}, {2, 1}})
		if err != nil {
			t.Fatal(err)
		}
		sum := new(big.Int)
		for _, x := range xs {
			sum.Add(sum, x.Micros)
		}
		if sum.Cmp(big.NewInt(n)) != 0 {
			t.Fatal(n, xs)
		}
		if n == 1 && xs[0].Micros.Int64() != 1 {
			t.Fatal("non deterministic remainder")
		}
	}
}
func TestDecimalNoFloat(t *testing.T) {
	for raw, want := range map[string]string{"123456789012345.123456": "123456789012345123456", "-0.0000005": "-1", "0.00000049": "0"} {
		got, err := Micros(raw)
		if err != nil || got.String() != want {
			t.Fatal(raw, got, err)
		}
	}
}

func TestExactTotalDoesNotRoundThroughDecimalText(t *testing.T) {
	for _, value := range []string{"0.0000004999999999999999999999999999999999999", "-0.0000004999999999999999999999999999999999999"} {
		r, ok := new(big.Rat).SetString(value)
		if !ok {
			t.Fatal(value)
		}
		if result := RatMicros(r); result.Sign() != 0 {
			t.Fatal("precision changed the total", value, result)
		}
		if r.RatString() == "0" {
			t.Fatal("rounding mutated the source")
		}
	}
}
