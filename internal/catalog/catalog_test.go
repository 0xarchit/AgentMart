// Tests for catalog validation and response mapping.
package catalog

import "testing"

func TestCheckStockRejectsNonPositiveQuantity(t *testing.T) {
	service := &Service{}
	_, err := service.CheckStock(t.Context(), "product", 0)
	if err == nil {
		t.Fatal("expected quantity validation error")
	}
}

func TestEscapeFilter(t *testing.T) {
	if got := escapeFilter("a*b,c"); got != `a\*b\,c` {
		t.Fatalf("escapeFilter() = %q", got)
	}
}
