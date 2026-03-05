package detector

import (
	"testing"
)

func TestEvaluateThreshold(t *testing.T) {
	fd := &FailureDetector{
		results: make(map[string]*CriterionResult),
	}

	tests := []struct {
		name      string
		value     float64
		threshold string
		want      bool
		wantErr   bool
	}{
		// Basic operators
		{"greater than pass", 5.0, "> 0", true, false},
		{"greater than fail", 0.0, "> 0", false, false},
		{"less than pass", 5.0, "< 10", true, false},
		{"less than fail", 15.0, "< 10", false, false},
		{"greater equal pass exact", 5.0, ">= 5", true, false},
		{"greater equal pass above", 6.0, ">= 5", true, false},
		{"greater equal fail", 4.0, ">= 5", false, false},
		{"less equal pass exact", 5.0, "<= 5", true, false},
		{"less equal pass below", 4.0, "<= 5", true, false},
		{"less equal fail", 6.0, "<= 5", false, false},
		{"equal pass", 5.0, "== 5", true, false},
		{"equal fail", 4.0, "== 5", false, false},
		{"not equal pass", 4.0, "!= 5", true, false},
		{"not equal fail", 5.0, "!= 5", false, false},

		// Whitespace handling
		{"extra spaces", 5.0, "  >=   5  ", true, false},
		{"no space after operator", 5.0, ">=5", true, false},

		// Decimal thresholds
		{"decimal threshold", 0.67, ">= 0.67", true, false},
		{"decimal threshold fail", 0.66, ">= 0.67", false, false},

		// Negative thresholds
		{"negative threshold", -5.0, "> -10", true, false},
		{"negative value pass", -3.0, "< 0", true, false},

		// Zero
		{"zero equals zero", 0.0, "== 0", true, false},

		// Error cases: malformed values
		{"invalid value NaN", 5.0, ">= NaN", false, true},
		{"invalid value Inf", 5.0, ">= Inf", false, true},
		{"invalid value -Inf", 5.0, ">= -Inf", false, true},
		{"invalid value text", 5.0, "> abc", false, true},
		{"empty value", 5.0, ">= ", false, true},
		{"invalid format no operator", 5.0, "5", false, true},
		{"invalid format just text", 5.0, "hello", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fd.evaluateThreshold(tt.value, tt.threshold)
			if (err != nil) != tt.wantErr {
				t.Errorf("evaluateThreshold(%v, %q) error = %v, wantErr %v", tt.value, tt.threshold, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("evaluateThreshold(%v, %q) = %v, want %v", tt.value, tt.threshold, got, tt.want)
			}
		})
	}
}
