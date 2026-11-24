package domain

import "strings"

// Currencies the ledger accepts. Keep in sync with the CHECK constraints in
// migrations/000002 and migrations/000003.
const (
	USD = "USD"
	EUR = "EUR"
	JPY = "JPY"
)

var supportedCurrencies = map[string]struct{}{
	USD: {},
	EUR: {},
	JPY: {},
}

// NormalizeCurrency trims and upper-cases a currency code.
func NormalizeCurrency(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// IsSupportedCurrency reports whether code is one of the currencies the ledger
// accepts. Normalize the code first.
func IsSupportedCurrency(code string) bool {
	_, ok := supportedCurrencies[code]
	return ok
}
