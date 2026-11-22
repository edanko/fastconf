package expr

import (
	"strconv"
	"strings"
)

// fragmentKind identifies one piece of a compiled template.
type fragmentKind uint8

const (
	fragmentLiteral fragmentKind = iota
	fragmentLookup
	fragmentExpression
)

// fragment is one piece of a compiled template: literal text, a ${path}
// lookup or a #{ expression } conditional.
type fragment struct {
	kind   fragmentKind
	text   string
	lookup lookup
	expr   *expression
}

// lookup is a parsed ${path} or ${path:default} placeholder.
type lookup struct {
	path string
	// def is the parsed default value; present reports whether a default was
	// written after a colon.
	def     any
	present bool
}

// compileTemplate parses source into a Template. depth tracks nesting through
// quoted templates inside conditional expressions.
func compileTemplate(source string, depth int) (*Template, error) {
	if depth > maxCompileDepth {
		return nil, compileErrorf(source, 0, "placeholder nesting exceeds %d levels", maxCompileDepth)
	}
	if !hasMarkers(source) {
		return &Template{source: source}, nil
	}

	// An unpack template is only recognized when it starts the value; the
	// whole value must then be exactly one placeholder.
	if strings.HasPrefix(source, "*${") {
		look, next, err := parseLookupAt(source, 1, len(source))
		if err != nil {
			return nil, err
		}
		if next != len(source) {
			return nil, compileErrorf(source, next, "array unpack *${...} must be the entire value; unexpected text follows the placeholder")
		}
		return &Template{
			source:    source,
			unpack:    true,
			fragments: []fragment{{kind: fragmentLookup, lookup: look}},
		}, nil
	}

	var fragments []fragment
	literalStart := 0
	for i := 0; i < len(source); {
		switch {
		case strings.HasPrefix(source[i:], "*${"):
			return nil, compileErrorf(source, i, "array unpack *${...} must be the entire value or the entire list element")
		case strings.HasPrefix(source[i:], "${"):
			if literalStart < i {
				fragments = append(fragments, fragment{kind: fragmentLiteral, text: source[literalStart:i]})
			}
			look, next, err := parseLookupAt(source, i, len(source))
			if err != nil {
				return nil, err
			}
			fragments = append(fragments, fragment{kind: fragmentLookup, lookup: look})
			i, literalStart = next, next
		case strings.HasPrefix(source[i:], "#{"):
			if literalStart < i {
				fragments = append(fragments, fragment{kind: fragmentLiteral, text: source[literalStart:i]})
			}
			parsed, next, err := parseConditionalAt(source, i, depth)
			if err != nil {
				return nil, err
			}
			fragments = append(fragments, fragment{kind: fragmentExpression, expr: parsed})
			i, literalStart = next, next
		default:
			i++
		}
	}
	if literalStart < len(source) {
		fragments = append(fragments, fragment{kind: fragmentLiteral, text: source[literalStart:]})
	}
	if len(fragments) == 0 {
		return &Template{source: source}, nil
	}
	return &Template{source: source, fragments: fragments}, nil
}

// parseLookupAt parses the ${...} placeholder starting at start, where start
// points at the dollar sign. limit bounds the scan and is the end of the
// enclosing template or conditional expression.
func parseLookupAt(source string, start, limit int) (lookup, int, error) {
	j := start + 2
	for j < limit {
		switch source[j] {
		case '}':
			parsed, err := parseLookupBody(source, start+2, j)
			if err != nil {
				return lookup{}, 0, err
			}
			return parsed, j + 1, nil
		case '\'', '"':
			next, err := skipQuoted(source, j, limit)
			if err != nil {
				return lookup{}, 0, err
			}
			j = next
		default:
			j++
		}
	}
	return lookup{}, 0, compileErrorf(source, start, "unclosed placeholder: missing '}'")
}

// parseLookupBody parses the text between "${" and "}" into a lookup. The body
// is a path with an optional ":" separated default.
func parseLookupBody(source string, start, end int) (lookup, error) {
	pathEnd := end
	colon := -1
	if relative := strings.IndexByte(source[start:end], ':'); relative >= 0 {
		colon = start + relative
		pathEnd = colon
	}
	if err := validateLookupPath(source, start, pathEnd); err != nil {
		return lookup{}, err
	}

	parsed := lookup{path: strings.TrimSpace(source[start:pathEnd])}
	if colon < 0 {
		return parsed, nil
	}
	value, err := parseDefault(source, colon+1, end)
	if err != nil {
		return lookup{}, err
	}
	parsed.def = value
	parsed.present = true
	return parsed, nil
}

// validateLookupPath checks that source[start:end] is a non-empty path without
// empty segments or syntax characters.
func validateLookupPath(source string, start, end int) error {
	path := strings.TrimSpace(source[start:end])
	if path == "" {
		return compileErrorf(source, start, "empty placeholder path")
	}
	for i := start; i < end; i++ {
		switch source[i] {
		case '{', '}', '\'', '"', '\\':
			return compileErrorf(source, i, "invalid character %q in placeholder path", source[i])
		}
	}
	for _, segment := range strings.Split(path, ".") {
		if strings.TrimSpace(segment) == "" {
			return compileErrorf(source, start, "placeholder path %q contains an empty segment", path)
		}
	}
	return nil
}

// parseDefault parses a typed default value: booleans, numbers, quoted strings,
// bare strings and flat arrays.
func parseDefault(source string, start, end int) (any, error) {
	start, end = trimSpaceRange(source, start, end)
	if start == end {
		return "", nil
	}
	switch source[start] {
	case '\'', '"':
		value, next, err := parseQuoted(source, start, end)
		if err != nil {
			return nil, err
		}
		next, _ = trimSpaceRange(source, next, end)
		if next != end {
			return nil, compileErrorf(source, next, "unexpected text after quoted default value")
		}
		return value, nil
	case '[':
		return parseArrayDefault(source, start, end)
	}

	text := source[start:end]
	switch {
	case strings.EqualFold(text, "true"):
		return true, nil
	case strings.EqualFold(text, "false"):
		return false, nil
	case text[0] == '-' || text[0] == '+' || (text[0] >= '0' && text[0] <= '9'):
		if number, err := strconv.ParseFloat(text, 64); err == nil {
			return number, nil
		}
	}
	return text, nil
}

// parseArrayDefault parses a flat array default such as [a, 2, true, "b,c"].
func parseArrayDefault(source string, start, end int) (any, error) {
	if source[end-1] != ']' {
		return nil, compileErrorf(source, start, "unclosed array default: missing ']'")
	}
	innerStart, innerEnd := start+1, end-1
	values := make([]any, 0, 4)
	for i := innerStart; i < innerEnd; {
		for i < innerEnd && isSpace(source[i]) {
			i++
		}
		if i == innerEnd {
			break
		}
		elementStart := i
		elementEnd := innerEnd
		foundComma := false
		for j := i; j < innerEnd; {
			switch source[j] {
			case '\'', '"':
				next, err := skipQuoted(source, j, innerEnd)
				if err != nil {
					return nil, err
				}
				j = next
				continue
			case ',':
				elementEnd = j
				foundComma = true
			}
			if foundComma {
				break
			}
			j++
		}
		value, err := parseArrayElement(source, elementStart, elementEnd)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
		if !foundComma {
			break
		}
		i = elementEnd + 1
	}
	return values, nil
}

// parseArrayElement parses one element of an array default with the same typing
// rules as parseDefault, except that nested arrays are rejected.
func parseArrayElement(source string, start, end int) (any, error) {
	start, end = trimSpaceRange(source, start, end)
	if start == end {
		return nil, compileErrorf(source, start, "empty element in array default")
	}
	switch source[start] {
	case '\'', '"':
		value, next, err := parseQuoted(source, start, end)
		if err != nil {
			return nil, err
		}
		next, _ = trimSpaceRange(source, next, end)
		if next != end {
			return nil, compileErrorf(source, next, "unexpected text after quoted array element")
		}
		return value, nil
	case '[':
		return nil, compileErrorf(source, start, "nested array defaults are not supported")
	}

	text := source[start:end]
	switch {
	case strings.EqualFold(text, "true"):
		return true, nil
	case strings.EqualFold(text, "false"):
		return false, nil
	case text[0] == '-' || text[0] == '+' || (text[0] >= '0' && text[0] <= '9'):
		if number, err := strconv.ParseFloat(text, 64); err == nil {
			return number, nil
		}
	}
	return text, nil
}

// parseConditionalAt parses the #{...} expression starting at start, where
// start points at the hash sign. It returns the parsed expression and the index
// just after the closing brace.
func parseConditionalAt(source string, start, compileDepth int) (*expression, int, error) {
	depth := 0
	closeBrace := -1
	for j := start + 1; j < len(source); {
		switch source[j] {
		case '{':
			depth++
			j++
		case '}':
			depth--
			if depth == 0 {
				closeBrace = j
			}
			j++
		case '\'', '"':
			next, err := skipQuoted(source, j, len(source))
			if err != nil {
				return nil, 0, err
			}
			j = next
		default:
			j++
		}
		if closeBrace >= 0 {
			break
		}
	}
	if closeBrace < 0 {
		return nil, 0, compileErrorf(source, start, "unclosed conditional expression: missing '}'")
	}
	parsed, err := parseExpression(source, start+2, closeBrace, compileDepth)
	if err != nil {
		return nil, 0, err
	}
	return parsed, closeBrace + 1, nil
}

// skipQuoted returns the index just after the quoted string that starts at
// start, honoring backslash escapes. It does not build the unescaped value.
func skipQuoted(source string, start, end int) (int, error) {
	quote := source[start]
	for i := start + 1; i < end; i++ {
		switch source[i] {
		case '\\':
			i++
		case quote:
			return i + 1, nil
		}
	}
	return 0, compileErrorf(source, start, "unterminated quoted string")
}

// parseQuoted parses and unescapes the quoted string that starts at start. It
// returns the unescaped value and the index just after the closing quote.
func parseQuoted(source string, start, end int) (string, int, error) {
	quote := source[start]
	var builder strings.Builder
	for i := start + 1; i < end; i++ {
		switch source[i] {
		case quote:
			return builder.String(), i + 1, nil
		case '\\':
			if i+1 >= end {
				return "", 0, compileErrorf(source, start, "unterminated quoted string")
			}
			i++
			switch source[i] {
			case '\\', '\'', '"':
				builder.WriteByte(source[i])
			case 'n':
				builder.WriteByte('\n')
			case 't':
				builder.WriteByte('\t')
			case 'r':
				builder.WriteByte('\r')
			default:
				return "", 0, compileErrorf(source, i-1, "unknown escape sequence \\%c", source[i])
			}
		default:
			builder.WriteByte(source[i])
		}
	}
	return "", 0, compileErrorf(source, start, "unterminated quoted string")
}

// trimSpaceRange trims surrounding whitespace from source[start:end].
func trimSpaceRange(source string, start, end int) (int, int) {
	for start < end && isSpace(source[start]) {
		start++
	}
	for end > start && isSpace(source[end-1]) {
		end--
	}
	return start, end
}

// isSpace reports whether c is ASCII whitespace.
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
