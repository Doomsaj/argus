package diff

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// ReportMeta is the header information of a spec check run.
type ReportMeta struct {
	Service, Against, BaseVersion, RevisionVersion string
	Mode, EffectiveMode, WarnUntil                 string
	Blocking, Exit                                 int
	UnusedWaivers                                  []string
	ConsumersUnavailable                           bool
}

const maxRows = 200 // GitLab notes are capped at 1,000,000 chars; keep the note readable

// Markdown renders argus-report.md, which is also the MR note body.
func Markdown(m ReportMeta, fs []Finding) string {
	var b strings.Builder
	verdict := "PASS"
	switch {
	case m.Blocking > 0 && m.EffectiveMode == "block":
		verdict = "BLOCKED"
	case m.Blocking > 0:
		verdict = "WOULD BLOCK after " + m.WarnUntil
	}
	fmt.Fprintf(&b, "### argus spec check: %s\n\n", verdict)
	fmt.Fprintf(&b, "`%s` revision `%s` against `%s` (version `%s`). Gate: `%s` (effective `%s`). Blocking findings: **%d**.\n\n",
		m.Service, m.RevisionVersion, m.Against, m.BaseVersion, m.Mode, m.EffectiveMode, m.Blocking)
	var main, info []Finding
	for _, f := range fs {
		if f.Level == "INFO" && f.Waived == NotWaived {
			info = append(info, f)
		} else {
			main = append(main, f)
		}
	}
	if len(main) > 0 {
		b.WriteString("| Severity | Rule | Location | Affected consumers | Waiver | Message |\n|---|---|---|---|---|---|\n")
		for i, f := range main {
			if i == maxRows {
				fmt.Fprintf(&b, "\n%d more rows in the `argus-report.md` job artifact.\n", len(main)-maxRows)
				break
			}
			sev := f.Level
			if f.Blocking {
				sev = "**" + sev + "** (blocking)"
			}
			cons := strings.Join(f.Consumers, ", ")
			if cons == "" {
				cons = "none known"
			}
			waiver := string(f.Waived)
			if f.Waiver != nil {
				waiver += fmt.Sprintf(" (%s, expires %s)", f.Waiver.ApprovedBy, f.Waiver.Expires.Format("2006-01-02"))
			}
			fmt.Fprintf(&b, "| %s | `%s` | `%s` | %s | %s | %s |\n", sev, f.Rule, f.Location, cons, waiver, cell(f.Text))
		}
	} else {
		b.WriteString("No breaking changes.\n")
	}
	if len(info) > 0 {
		fmt.Fprintf(&b, "\n<details><summary>%d non-breaking changes</summary>\n\n", len(info))
		for _, f := range info {
			fmt.Fprintf(&b, "- `%s` `%s`: %s\n", f.Rule, f.Location, cell(f.Text))
		}
		b.WriteString("\n</details>\n")
	}
	if len(m.UnusedWaivers) > 0 {
		fmt.Fprintf(&b, "\nUnused waivers (remove from `argus.yaml`): %s\n", strings.Join(m.UnusedWaivers, "; "))
	}
	if m.ConsumersUnavailable {
		b.WriteString("\nPact Broker unreachable: affected consumers unknown.\n")
	}
	b.WriteString("\n\"none known\" means no consumer pact covers the endpoint. Consumers not onboarded to Pact are not listed. Docs: 06-argus-cli.md.\n")
	return b.String()
}

func cell(s string) string { return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s) }

type junitSuites struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Failure   *junitMessage `xml:"failure,omitempty"`
	Skipped   *junitMessage `xml:"skipped,omitempty"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

// JUnit renders argus-report.junit.xml for GitLab reports:junit. Blocking findings are
// failures; in warn mode they are reported as failures too so the MR widget shows them,
// while the job itself exits 0 (the widget does not fail the pipeline).
func JUnit(service string, fs []Finding) ([]byte, error) {
	s := junitSuite{Name: "argus spec check " + service}
	for _, f := range fs {
		if f.Level == "INFO" {
			continue
		}
		c := junitCase{Name: f.Rule + " " + f.Location, Classname: "argus.spec." + f.Operation}
		switch {
		case f.Blocking:
			c.Failure = &junitMessage{Message: f.Level + " " + f.Rule, Text: f.Text}
			s.Failures++
		case f.Waived == Applied:
			c.Skipped = &junitMessage{Message: "waived by " + f.Waiver.ApprovedBy + ": " + f.Waiver.Reason}
		}
		s.Cases = append(s.Cases, c)
	}
	if len(s.Cases) == 0 {
		s.Cases = []junitCase{{Name: "no breaking changes", Classname: "argus.spec"}}
	}
	s.Tests = len(s.Cases)
	out, err := xml.MarshalIndent(junitSuites{Suites: []junitSuite{s}}, "", "  ")
	return append([]byte(xml.Header), out...), err
}
