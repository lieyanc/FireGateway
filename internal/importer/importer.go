// Package importer converts configurations from other forwarders into
// FireGateway rules: rinetd.conf files and JSON from the Node.js FireProxy
// (or FireGateway exports), including partial snippets pasted by hand.
package importer

import (
	"fmt"
	"strings"

	"github.com/lieyanc/FireGateway/internal/config"
)

const (
	FormatAuto   = "auto"
	FormatJSON   = "json"
	FormatRinetd = "rinetd"
)

// Warning describes input that was skipped or changed during conversion.
// Line is set for rinetd input, Index (1-based rule position) for JSON.
type Warning struct {
	Line    int    `json:"line,omitempty"`
	Index   int    `json:"index,omitempty"`
	Message string `json:"message"`
}

type Result struct {
	Format   string        `json:"format"`
	Rules    []config.Rule `json:"rules"`
	Warnings []Warning     `json:"warnings"`
}

// Parse converts text in the given format; FormatAuto (or "") detects it.
// Rules in the result are normalized and valid, except that ids may be
// empty; rules that could not be converted are dropped with a warning.
func Parse(format, text string) (*Result, error) {
	if format == "" || format == FormatAuto {
		format = Detect(text)
	}
	var (
		res *Result
		err error
	)
	switch format {
	case FormatJSON:
		res, err = parseJSON(text)
	case FormatRinetd:
		res, err = parseRinetd(text)
	default:
		return nil, &config.FieldError{Field: "format", Msg: "format must be auto, json or rinetd"}
	}
	if err != nil {
		return nil, err
	}
	res.finalize()
	return res, nil
}

// Detect guesses the format: JSON documents and snippets start with a
// bracket or a quoted key, while rinetd lines never do.
func Detect(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.ContainsAny(line[:1], `{["`) {
			return FormatJSON
		}
		return FormatRinetd
	}
	return FormatRinetd
}

// entry is a converted rule plus where it came from, for warnings.
type entry struct {
	rule  config.Rule
	line  int
	index int
}

func (res *Result) warn(e *entry, format string, args ...any) {
	res.Warnings = append(res.Warnings, Warning{Line: e.line, Index: e.index, Message: fmt.Sprintf(format, args...)})
}

// collect runs the shared checks over converted entries and stores the
// survivors: invalid ids are dropped so new ones get generated, invalid rules
// are skipped, and rules whose listeners overlap an earlier one are disabled
// so the import as a whole can still be applied.
func (res *Result) collect(entries []entry) {
	res.Rules = make([]config.Rule, 0, len(entries))
	ids := make(map[config.RuleID]bool)
	for i := range entries {
		e := &entries[i]
		r := &e.rule
		if r.ID != "" {
			if config.ValidateID(r.ID) != nil {
				res.warn(e, "id %q is not a valid FireGateway id; a new id will be assigned", r.ID)
				r.ID = ""
			} else if ids[r.ID] {
				res.warn(e, "duplicate id %q; a new id will be assigned", r.ID)
				r.ID = ""
			}
		}
		r.Normalize()
		check := *r
		if check.ID == "" {
			check.ID = "0"
		}
		if err := check.Validate(); err != nil {
			res.warn(e, "skipped: %s", err)
			continue
		}
		if r.Active() {
			for j := range res.Rules {
				if o := &res.Rules[j]; o.Active() && o.ListenOverlaps(r) {
					res.warn(e, "listens on the same %s port as an earlier rule; imported as disabled", r.Type)
					r.Status = config.StatusInactive
					break
				}
			}
		}
		if r.ID != "" {
			ids[r.ID] = true
		}
		res.Rules = append(res.Rules, *r)
	}
}

// finalize guarantees non-nil slices so the API returns [] rather than null.
func (res *Result) finalize() {
	if res.Rules == nil {
		res.Rules = []config.Rule{}
	}
	if res.Warnings == nil {
		res.Warnings = []Warning{}
	}
}
