package serve

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// parseDecimal parses a decimal string into a *decimal.Decimal.
func parseDecimal(s string) (*decimal.Decimal, error) {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return nil, fmt.Errorf("parse decimal: %w", err)
	}
	return &d, nil
}
