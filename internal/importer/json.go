package importer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/lieyanc/FireGateway/internal/config"
)

// parseJSON accepts a whole FireProxy/FireGateway config, an export, a bare
// rule array, a single rule, or a snippet cut out of any of these, such as
// `"forward": [...]` or a few `{...},` objects. Comments and trailing commas
// are tolerated since hand-edited files often have them.
func parseJSON(text string) (*Result, error) {
	clean := strings.TrimSpace(stripJSONExtras(text))
	clean = strings.TrimSuffix(clean, ",")
	if clean == "" {
		return nil, &config.FieldError{Field: "text", Msg: "input is empty"}
	}
	// A snippet only parses once wrapped in brackets or braces. Wrapping is
	// tried only when the text alone is not valid JSON, so a document without
	// rules is reported as such instead of being read as a single rule.
	var (
		doc any
		err error
	)
	for i, cand := range []string{clean, "[" + clean + "]", "{" + clean + "}"} {
		var e error
		if doc, e = decodeJSON(cand); e == nil {
			err = nil
			break
		}
		if i == 0 {
			err = jsonError(clean, e)
		}
	}
	if err != nil {
		return nil, &config.FieldError{Field: "text", Msg: err.Error()}
	}
	list, ok := ruleList(doc)
	if !ok {
		return nil, &config.FieldError{Field: "text", Msg: `no rules found: expected a config with a "forward" array, an array of rules or a rule object`}
	}
	if len(list) == 0 {
		return nil, &config.FieldError{Field: "text", Msg: "the forward array is empty"}
	}
	res := &Result{Format: FormatJSON}
	entries := make([]entry, 0, len(list))
	for i, raw := range list {
		e := entry{index: i + 1}
		if convertJSONRule(res, &e, raw) {
			entries = append(entries, e)
		}
	}
	res.collect(entries)
	return res, nil
}

func decodeJSON(s string) (any, error) {
	var v any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errTrailing
	}
	return v, nil
}

var errTrailing = errors.New("unexpected content after the first value")

// ruleList extracts the rule objects from a decoded document.
func ruleList(v any) ([]json.RawMessage, bool) {
	switch t := v.(type) {
	case map[string]any:
		if f, ok := t["rules"]; ok {
			return ruleList(f)
		}
		if f, ok := t["forward"]; ok {
			return ruleList(f)
		}
		if looksLikeRule(t) {
			b, _ := json.Marshal(t)
			return []json.RawMessage{b}, true
		}
	case []any:
		out := make([]json.RawMessage, 0, len(t))
		for _, item := range t {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, false
			}
			b, _ := json.Marshal(m)
			out = append(out, b)
		}
		return out, true
	}
	return nil, false
}

func looksLikeRule(m map[string]any) bool {
	for _, k := range []string{"localPort", "localPortRange", "targetHost", "targetPort"} {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

// convertJSONRule applies the Node.js FireProxy's semantics where they differ
// from FireGateway's and decodes the rule. It reports false to skip the rule.
func convertJSONRule(res *Result, e *entry, raw json.RawMessage) bool {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		res.warn(e, "skipped: %s", err)
		return false
	}
	// FireProxy only started rules whose status was exactly "active".
	switch s, _ := m["status"].(string); s {
	case config.StatusActive, config.StatusInactive:
	case "":
		res.warn(e, "no status; imported as disabled, as FireProxy did not start it")
		m["status"] = config.StatusInactive
	default:
		res.warn(e, "status %q imported as disabled, as FireProxy did not start it", s)
		m["status"] = config.StatusInactive
	}
	if t, ok := m["type"].(string); ok {
		m["type"] = strings.ToLower(strings.TrimSpace(t))
	}
	// Node accepted numeric strings wherever it expected a port.
	for _, k := range []string{"localPort", "targetPort"} {
		m[k] = numberish(m[k])
	}
	for _, k := range []string{"localPortRange", "targetPortRange"} {
		if arr, ok := m[k].([]any); ok {
			for i := range arr {
				arr[i] = numberish(arr[i])
			}
		}
	}
	// FireProxy used ranges only when both were present and otherwise fell
	// back to the single ports.
	_, lr := m["localPortRange"]
	_, tr := m["targetPortRange"]
	if lr != tr && m["localPort"] != nil && m["targetPort"] != nil {
		res.warn(e, "only one port range given; using localPort and targetPort as FireProxy did")
		delete(m, "localPortRange")
		delete(m, "targetPortRange")
	}
	b, _ := json.Marshal(m)
	if err := json.Unmarshal(b, &e.rule); err != nil {
		res.warn(e, "skipped: %s", describeTypeError(err))
		return false
	}
	return true
}

func numberish(v any) any {
	if s, ok := v.(string); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return n
		}
	}
	return v
}

func describeTypeError(err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) && te.Field != "" {
		return fmt.Sprintf("%s has the wrong type (%s)", te.Field, te.Value)
	}
	return err.Error()
}

// jsonError adds a line number to syntax errors.
func jsonError(text string, err error) error {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		off := min(int(se.Offset), len(text))
		return fmt.Errorf("invalid JSON at line %d: %s", strings.Count(text[:off], "\n")+1, se.Error())
	}
	return fmt.Errorf("invalid JSON: %s", err)
}

// stripJSONExtras removes // and /* */ comments and trailing commas outside
// of strings, keeping newlines so error line numbers still match the input.
func stripJSONExtras(s string) string {
	return scanJSON(scanJSON(s, true), false)
}

// scanJSON drops comments (comments=true) or trailing commas (false). The
// passes are separate so a comment between a comma and a bracket is gone
// before commas are examined.
func scanJSON(s string, comments bool) string {
	var b strings.Builder
	b.Grow(len(s))
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			b.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			b.WriteByte(c)
		case comments && c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			i--
		case comments && c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				end = len(s) - i - 2
			}
			b.WriteString(strings.Repeat("\n", strings.Count(s[i:i+2+end], "\n")))
			i += end + 3
		case !comments && c == ',':
			j := i + 1
			for j < len(s) && strings.IndexByte(" \t\r\n", s[j]) >= 0 {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
