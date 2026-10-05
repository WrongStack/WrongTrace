package models

import (
	"fmt"
	"testing"
)

func TestImportPricesUseUpsertNormalization(t *testing.T) {
	for _, rates := range [][3]int{{2, 8, 1}, {-2, 8, 0}, {2, -8, 1}, {2, 8, -1}, {0, 0, 0}} {
		r := NewRegistry()
		body := fmt.Sprintf(`{"fixture":{"models":{"fixture-model":{"cost":{"input":%d,"output":%d,"cache_read":%d},"limit":{"context":1000}}}}}`, rates[0], rates[1], rates[2])
		if _, err := r.ImportModelsDevJSON([]byte(body)); err != nil {
			t.Fatal(err)
		}
		got, ok := r.Get("fixture/fixture-model")
		if !ok {
			t.Fatal("imported model missing")
		}
		if got.InputPricePerM != sanitizePrice(float64(rates[0])) || got.OutputPricePerM != sanitizePrice(float64(rates[1])) || got.CacheReadPricePerM != sanitizePrice(float64(rates[2])) {
			t.Fatalf("stored unsanitized rates: %+v", got)
		}
		want := sanitizePrice(float64(rates[0])) + sanitizePrice(float64(rates[1]))
		if cost := r.CalculateCost("fixture-model", 1000000, 1000000); cost != want {
			t.Fatalf("cost=%g want=%g", cost, want)
		}
	}
}
