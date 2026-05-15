package main

import "testing"

func TestOperationClassifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		classes   map[string]string
		toolName  string
		wantClass OperationClass
	}{
		{
			name: "explicit read entry",
			classes: map[string]string{
				"create_refund": "read",
			},
			toolName:  "create_refund",
			wantClass: OperationClassRead,
		},
		{
			name: "explicit write entry",
			classes: map[string]string{
				"get_payment": "write",
			},
			toolName:  "get_payment",
			wantClass: OperationClassWrite,
		},
		{
			name:      "get prefix uses read heuristic",
			toolName:  "get_payment",
			wantClass: OperationClassRead,
		},
		{
			name:      "read prefix uses read heuristic",
			toolName:  "read_payment",
			wantClass: OperationClassRead,
		},
		{
			name:      "list prefix uses read heuristic",
			toolName:  "list_payments",
			wantClass: OperationClassRead,
		},
		{
			name:      "non matching prefix defaults to write",
			toolName:  "create_refund",
			wantClass: OperationClassWrite,
		},
		{
			name: "explicit entry overrides heuristic",
			classes: map[string]string{
				"get_payment": "write",
			},
			toolName:  "get_payment",
			wantClass: OperationClassWrite,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			classifier := NewOperationClassifier(tt.classes)

			if got := classifier.Classify(tt.toolName); got != tt.wantClass {
				t.Fatalf("Classify(%q) = %v, want %v", tt.toolName, got, tt.wantClass)
			}
		})
	}
}
