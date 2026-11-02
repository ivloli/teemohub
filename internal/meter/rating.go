package meter

import (
	"fmt"
	"math"
)

// Prices are nanoCNY per token (1 CNY = 1e8 nanoCNY). A 2 CNY / 1M token
// list price is therefore exactly 200 nanoCNY per token, so settlement is
// integer arithmetic with no floating point drift.
func Cost(tokens, nanoPerToken int64) int64 {
	if tokens <= 0 || nanoPerToken <= 0 {
		return 0
	}
	return tokens * nanoPerToken
}

func CostFor(input, output, inPrice, outPrice int64) int64 {
	return Cost(input, inPrice) + Cost(output, outPrice)
}

// CostForCached splits the prompt into cached and uncached buckets. The cached
// count comes from upstream usage (New API relays it); it is clamped to the
// prompt size and ignored when no cached price is configured.
func CostForCached(input, cached, output, inPrice, cachedPrice, outPrice int64) int64 {
	if cached < 0 {
		cached = 0
	}
	if cached > input {
		cached = input
	}
	if cachedPrice <= 0 {
		cachedPrice = inPrice
	}
	return Cost(input-cached, inPrice) + Cost(cached, cachedPrice) + Cost(output, outPrice)
}

func FormatCNY(nano int64) string {
	sign := ""
	if nano < 0 {
		sign = "-"
		nano = -nano
	}
	yuan := float64(nano) / 1e8
	return sign + fmt.Sprintf("¥%.6f", math.Round(yuan*1e6)/1e6)
}
