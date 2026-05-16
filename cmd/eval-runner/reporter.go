package main

import (
	"bytes"
	"strconv"
	"strings"
	"text/template"
)

var reportTemplate = template.Must(template.New("report").Parse(`| Case | Status |
| --- | --- |
{{- range .Rows }}
| {{ .Name }} | {{ .Status }} |
{{- end }}

{{ .PassedCount }}/{{ .TotalCount }} cases passed
{{- range .FailedCases }}

## {{ .Name }}
| Check | Expected | Observed |
| --- | --- | --- |
{{- range .Failures }}
| {{ .Check }} | {{ .Expected }} | {{ .Observed }} |
{{- end }}
{{- end }}

{{ .Verdict }}`))

func GenerateReport(results []CaseResult) string {
	passedCount := 0
	failedCases := make([]reportFailureCase, 0)
	rows := make([]reportRow, 0, len(results))
	for _, result := range results {
		status := "FAIL"
		if result.Passed {
			status = "PASS"
			passedCount++
		} else {
			failedCases = append(failedCases, sanitizeFailureCase(result))
		}

		rows = append(rows, reportRow{
			Name:   sanitizeMarkdownCell(result.Name),
			Status: status,
		})
	}

	data := reportData{
		Rows:        rows,
		PassedCount: passedCount,
		TotalCount:  len(results),
		FailedCases: failedCases,
		Verdict:     finalVerdict(len(failedCases)),
	}

	var b bytes.Buffer
	if err := reportTemplate.Execute(&b, data); err != nil {
		panic(err)
	}

	return b.String()
}

func ExitCode(results []CaseResult) int {
	for _, result := range results {
		if !result.Passed {
			return 1
		}
	}
	return 0
}

func finalVerdict(failedCount int) string {
	if failedCount == 0 {
		return "PASS"
	}

	return "FAIL: " + strconv.Itoa(failedCount) + " case(s) failed"
}

var markdownCellSanitizer = strings.NewReplacer(
	"\r\n", "<br>",
	"\r", "<br>",
	"\n", "<br>",
	"|", "\\|",
)

func sanitizeMarkdownCell(value string) string {
	return markdownCellSanitizer.Replace(value)
}

func sanitizeFailureCase(result CaseResult) reportFailureCase {
	failures := make([]reportFailureRow, 0, len(result.Failures))
	for _, failure := range result.Failures {
		failures = append(failures, reportFailureRow{
			Check:    sanitizeMarkdownCell(failure.Check),
			Expected: sanitizeMarkdownCell(failure.Expected),
			Observed: sanitizeMarkdownCell(failure.Observed),
		})
	}

	return reportFailureCase{
		Name:     sanitizeMarkdownCell(result.Name),
		Failures: failures,
	}
}

type reportRow struct {
	Name   string
	Status string
}

type reportFailureCase struct {
	Name     string
	Failures []reportFailureRow
}

type reportFailureRow struct {
	Check    string
	Expected string
	Observed string
}

type reportData struct {
	Rows        []reportRow
	PassedCount int
	TotalCount  int
	FailedCases []reportFailureCase
	Verdict     string
}
