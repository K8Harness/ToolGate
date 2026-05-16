package main

import (
	"strings"
	"testing"
)

func TestReporterAllPassProducesPassVerdictAndZeroExitCode(t *testing.T) {
	results := []CaseResult{
		{Name: "small-refund-allow", Passed: true},
		{Name: "large-refund-approval", Passed: true},
	}

	report := GenerateReport(results)

	if lastLine(report) != "PASS" {
		t.Fatalf("lastLine(report) = %q, want %q", lastLine(report), "PASS")
	}
	if ExitCode(results) != 0 {
		t.Fatalf("ExitCode(results) = %d, want 0", ExitCode(results))
	}
	if !strings.Contains(report, "2/2 cases passed") {
		t.Fatalf("report = %q, want pass-rate line", report)
	}
	assertSummaryRows(t, report, []string{
		"| small-refund-allow | PASS |",
		"| large-refund-approval | PASS |",
	})
}

func TestReporterFailureProducesFailureVerdictAndDetails(t *testing.T) {
	results := []CaseResult{
		{Name: "small-refund-allow", Passed: true},
		{
			Name:   "delete-customer-deny",
			Passed: false,
			Failures: []CheckFailure{
				{
					Check:    "policyOutcome",
					Expected: "deny",
					Observed: "allow",
				},
				{
					Check:    "mustNotInclude",
					Expected: "send_slack_message",
					Observed: "send_slack_message",
				},
			},
		},
		{Name: "slack-pii-redact", Passed: true},
	}

	report := GenerateReport(results)

	if lastLine(report) != "FAIL: 1 case(s) failed" {
		t.Fatalf("lastLine(report) = %q, want %q", lastLine(report), "FAIL: 1 case(s) failed")
	}
	if ExitCode(results) != 1 {
		t.Fatalf("ExitCode(results) = %d, want 1", ExitCode(results))
	}
	assertSummaryRows(t, report, []string{
		"| small-refund-allow | PASS |",
		"| delete-customer-deny | FAIL |",
		"| slack-pii-redact | PASS |",
	})
	assertReportOrder(t, report, []string{
		"| Case | Status |",
		"| --- | --- |",
		"| small-refund-allow | PASS |",
		"| delete-customer-deny | FAIL |",
		"| slack-pii-redact | PASS |",
		"2/3 cases passed",
		"## delete-customer-deny",
		"| Check | Expected | Observed |",
		"| --- | --- | --- |",
		"| policyOutcome | deny | allow |",
		"| mustNotInclude | send_slack_message | send_slack_message |",
		"FAIL: 1 case(s) failed",
	})
}

func TestReporterFailureDetailsRemainInInputOrderAndVerdictIsLastLine(t *testing.T) {
	results := []CaseResult{
		{
			Name:   "case-zeta",
			Passed: false,
			Failures: []CheckFailure{
				{
					Check:    "policyOutcome",
					Expected: "allow",
					Observed: "deny",
				},
			},
		},
		{Name: "case-alpha", Passed: true},
		{
			Name:   "case-beta",
			Passed: false,
			Failures: []CheckFailure{
				{
					Check:    "mustInclude",
					Expected: "create_ticket -> send_slack_message",
					Observed: "create_ticket",
				},
			},
		},
	}

	report := GenerateReport(results)

	assertReportOrder(t, report, []string{
		"| Case | Status |",
		"| --- | --- |",
		"| case-zeta | FAIL |",
		"| case-alpha | PASS |",
		"| case-beta | FAIL |",
		"1/3 cases passed",
		"## case-zeta",
		"| policyOutcome | allow | deny |",
		"## case-beta",
		"| mustInclude | create_ticket -> send_slack_message | create_ticket |",
		"FAIL: 2 case(s) failed",
	})

	if lastLine(report) != "FAIL: 2 case(s) failed" {
		t.Fatalf("lastLine(report) = %q, want %q", lastLine(report), "FAIL: 2 case(s) failed")
	}
}

func TestReporterCountsMultipleFailuresInVerdict(t *testing.T) {
	results := []CaseResult{
		{Name: "case-1", Passed: false},
		{Name: "case-2", Passed: true},
		{Name: "case-3", Passed: false},
	}

	report := GenerateReport(results)

	if lastLine(report) != "FAIL: 2 case(s) failed" {
		t.Fatalf("lastLine(report) = %q, want %q", lastLine(report), "FAIL: 2 case(s) failed")
	}
}

func TestReporterSanitizesSummaryTableCells(t *testing.T) {
	results := []CaseResult{
		{Name: "case|one\r\nline-two", Passed: true},
	}

	report := GenerateReport(results)

	if !strings.Contains(report, "| case\\|one<br>line-two | PASS |") {
		t.Fatalf("report = %q, want sanitized summary row", report)
	}
}

func TestReporterSanitizesFailureDetailCells(t *testing.T) {
	results := []CaseResult{
		{
			Name:   "broken|case\r\nname",
			Passed: false,
			Failures: []CheckFailure{
				{
					Check:    "policyOutcome",
					Expected: "allow|review\r\nlater",
					Observed: "deny\nnow",
				},
			},
		},
	}

	report := GenerateReport(results)

	if !strings.Contains(report, "| policyOutcome | allow\\|review<br>later | deny<br>now |") {
		t.Fatalf("report = %q, want sanitized failure detail row", report)
	}
	if !strings.Contains(report, "## broken\\|case<br>name") {
		t.Fatalf("report = %q, want sanitized failed case header", report)
	}
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	return lines[len(lines)-1]
}

func assertSummaryRows(t *testing.T, report string, rows []string) {
	t.Helper()

	for _, row := range rows {
		if !strings.Contains(report, row) {
			t.Fatalf("report = %q, want summary row %q", report, row)
		}
	}
}

func assertReportOrder(t *testing.T, report string, fragments []string) {
	t.Helper()

	lastIndex := -1
	for _, fragment := range fragments {
		index := strings.Index(report, fragment)
		if index == -1 {
			t.Fatalf("report = %q, want fragment %q", report, fragment)
		}
		if index <= lastIndex {
			t.Fatalf("report = %q, fragment %q appeared out of order", report, fragment)
		}
		lastIndex = index
	}
}
