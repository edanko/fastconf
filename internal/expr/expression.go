package expr

import (
	"fmt"
	"strconv"
	"strings"
)

// expressionKind identifies one node of a conditional expression tree.
type expressionKind uint8

const (
	expressionLiteral expressionKind = iota
	expressionVariable
	expressionLookup
	expressionTemplate
	expressionNot
	expressionNegate
	expressionAnd
	expressionOr
	expressionCompare
	expressionTernary
)

// compareOp is one of the comparison operators.
type compareOp uint8

const (
	compareEqual compareOp = iota
	compareNotEqual
	compareGreater
	compareGreaterEqual
	compareLess
	compareLessEqual
)

// expression is a node of a parsed conditional expression tree.
type expression struct {
	kind expressionKind
	// literal holds the value of an expressionLiteral node.
	literal any
	// name holds the variable path of an expressionVariable node.
	name string
	// lookup holds the placeholder of an expressionLookup node.
	lookup lookup
	// template holds the compiled quoted template of an expressionTemplate
	// node. Quoted strings without markers are stored as literals instead.
	template *Template
	op       compareOp
	left     *expression
	right    *expression
	cond     *expression
}

// tokenKind identifies a lexical token of a conditional expression.
type tokenKind uint8

const (
	tokenEOF tokenKind = iota
	tokenIdentifier
	tokenNumber
	tokenString
	tokenLookup
	tokenNot
	tokenAnd
	tokenOr
	tokenTrue
	tokenFalse
	tokenLeftParen
	tokenRightParen
	tokenQuestion
	tokenColon
	tokenEqual
	tokenNotEqual
	tokenGreater
	tokenGreaterEqual
	tokenLess
	tokenLessEqual
	tokenMinus
)

// token is one lexical token. offset is a byte offset in the template source.
type token struct {
	kind   tokenKind
	offset int
	text   string
	number float64
	lookup lookup
}

// lexer scans a conditional expression inside source[start:end].
type lexer struct {
	source string
	pos    int
	end    int
}

// next returns the next token or a *CompileError for an invalid character.
func (l *lexer) next() (token, error) {
	for l.pos < l.end && isSpace(l.source[l.pos]) {
		l.pos++
	}
	if l.pos >= l.end {
		return token{kind: tokenEOF, offset: l.end}, nil
	}

	start := l.pos
	switch c := l.source[l.pos]; c {
	case '(':
		l.pos++
		return token{kind: tokenLeftParen, offset: start}, nil
	case ')':
		l.pos++
		return token{kind: tokenRightParen, offset: start}, nil
	case '?':
		l.pos++
		return token{kind: tokenQuestion, offset: start}, nil
	case ':':
		l.pos++
		return token{kind: tokenColon, offset: start}, nil
	case '-':
		l.pos++
		return token{kind: tokenMinus, offset: start}, nil
	case '!':
		l.pos++
		if l.pos < l.end && l.source[l.pos] == '=' {
			l.pos++
			return token{kind: tokenNotEqual, offset: start}, nil
		}
		return token{kind: tokenNot, offset: start}, nil
	case '=':
		l.pos++
		if l.pos < l.end && l.source[l.pos] == '=' {
			l.pos++
			return token{kind: tokenEqual, offset: start}, nil
		}
		return token{}, compileErrorf(l.source, start, "expected '==' in comparison")
	case '>':
		l.pos++
		if l.pos < l.end && l.source[l.pos] == '=' {
			l.pos++
			return token{kind: tokenGreaterEqual, offset: start}, nil
		}
		return token{kind: tokenGreater, offset: start}, nil
	case '<':
		l.pos++
		if l.pos < l.end && l.source[l.pos] == '=' {
			l.pos++
			return token{kind: tokenLessEqual, offset: start}, nil
		}
		return token{kind: tokenLess, offset: start}, nil
	case '\'', '"':
		value, next, err := parseQuoted(l.source, l.pos, l.end)
		if err != nil {
			return token{}, err
		}
		l.pos = next
		return token{kind: tokenString, offset: start, text: value}, nil
	case '$':
		if l.pos+1 < l.end && l.source[l.pos+1] == '{' {
			parsed, next, err := parseLookupAt(l.source, l.pos, l.end)
			if err != nil {
				return token{}, err
			}
			l.pos = next
			return token{kind: tokenLookup, offset: start, lookup: parsed}, nil
		}
		return token{}, compileErrorf(l.source, start, "unexpected '$' in expression; write ${path} for a placeholder")
	}

	if c := l.source[start]; c >= '0' && c <= '9' {
		return l.number()
	}
	if isIdentifierStart(l.source[start]) {
		return l.identifier()
	}
	return token{}, compileErrorf(l.source, start, "unexpected character %q in expression", l.source[start])
}

// number scans an unsigned numeric literal.
func (l *lexer) number() (token, error) {
	start := l.pos
	for l.pos < l.end && l.source[l.pos] >= '0' && l.source[l.pos] <= '9' {
		l.pos++
	}
	if l.pos < l.end && l.source[l.pos] == '.' {
		l.pos++
		for l.pos < l.end && l.source[l.pos] >= '0' && l.source[l.pos] <= '9' {
			l.pos++
		}
	}
	if l.pos < l.end && (l.source[l.pos] == 'e' || l.source[l.pos] == 'E') {
		saved := l.pos
		l.pos++
		if l.pos < l.end && (l.source[l.pos] == '+' || l.source[l.pos] == '-') {
			l.pos++
		}
		digits := l.pos
		for l.pos < l.end && l.source[l.pos] >= '0' && l.source[l.pos] <= '9' {
			l.pos++
		}
		if l.pos == digits {
			// Not an exponent after all; leave the suffix for the caller.
			l.pos = saved
		}
	}
	text := l.source[start:l.pos]
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return token{}, compileErrorf(l.source, start, "invalid number %q", text)
	}
	return token{kind: tokenNumber, offset: start, number: value}, nil
}

// identifier scans a variable path or a keyword.
func (l *lexer) identifier() (token, error) {
	start := l.pos
	for l.pos < l.end && isIdentifierPart(l.source[l.pos]) {
		l.pos++
	}
	text := l.source[start:l.pos]
	switch {
	case strings.EqualFold(text, "and"):
		return token{kind: tokenAnd, offset: start}, nil
	case strings.EqualFold(text, "or"):
		return token{kind: tokenOr, offset: start}, nil
	case strings.EqualFold(text, "true"):
		return token{kind: tokenTrue, offset: start}, nil
	case strings.EqualFold(text, "false"):
		return token{kind: tokenFalse, offset: start}, nil
	}
	for segment := range strings.SplitSeq(text, ".") {
		if segment == "" {
			return token{}, compileErrorf(l.source, start, "variable path %q contains an empty segment", text)
		}
	}
	return token{kind: tokenIdentifier, offset: start, text: text}, nil
}

// parser is a recursive-descent parser for conditional expressions.
type parser struct {
	source       string
	lexer        *lexer
	current      token
	depth        int
	compileDepth int
}

// parseExpression parses source[start:end] as one complete expression.
// compileDepth is the quoted template nesting depth of the enclosing template.
func parseExpression(source string, start, end, compileDepth int) (*expression, error) {
	p := &parser{
		source:       source,
		lexer:        &lexer{source: source, pos: start, end: end},
		compileDepth: compileDepth,
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	if p.current.kind == tokenEOF {
		return nil, compileErrorf(source, start, "empty conditional expression")
	}
	parsed, err := p.parseTernary()
	if err != nil {
		return nil, err
	}
	if p.current.kind != tokenEOF {
		return nil, compileErrorf(source, p.current.offset, "unexpected %s in expression", tokenName(p.current))
	}
	return parsed, nil
}

// advance reads the next token.
func (p *parser) advance() error {
	next, err := p.lexer.next()
	if err != nil {
		return err
	}
	p.current = next
	return nil
}

// enter tracks expression nesting depth for a recursive parse step.
func (p *parser) enter() error {
	p.depth++
	if p.depth > maxExpressionDepth {
		return compileErrorf(p.source, p.current.offset, "expression nesting exceeds %d levels", maxExpressionDepth)
	}
	return nil
}

// leave undoes one enter call.
func (p *parser) leave() {
	p.depth--
}

// parseTernary parses condition ? then : else, right associatively.
func (p *parser) parseTernary() (*expression, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer p.leave()

	condition, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.current.kind != tokenQuestion {
		return condition, nil
	}
	offset := p.current.offset
	if err := p.advance(); err != nil {
		return nil, err
	}
	thenBranch, err := p.parseTernary()
	if err != nil {
		return nil, err
	}
	if p.current.kind != tokenColon {
		return nil, compileErrorf(p.source, offset, "conditional expression needs ':' after the '?' branch")
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	elseBranch, err := p.parseTernary()
	if err != nil {
		return nil, err
	}
	return &expression{kind: expressionTernary, cond: condition, left: thenBranch, right: elseBranch}, nil
}

// parseOr parses a chain of "or" operands.
func (p *parser) parseOr() (*expression, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.current.kind == tokenOr {
		if err := p.advance(); err != nil {
			return nil, err
		}
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &expression{kind: expressionOr, left: left, right: right}
	}
	return left, nil
}

// parseAnd parses a chain of "and" operands.
func (p *parser) parseAnd() (*expression, error) {
	left, err := p.parseComparison()
	if err != nil {
		return nil, err
	}
	for p.current.kind == tokenAnd {
		if err := p.advance(); err != nil {
			return nil, err
		}
		right, err := p.parseComparison()
		if err != nil {
			return nil, err
		}
		left = &expression{kind: expressionAnd, left: left, right: right}
	}
	return left, nil
}

// parseComparison parses one optional comparison. Comparisons do not chain:
// "1 < 2 < 3" is a syntax error because the second comparison is left over.
func (p *parser) parseComparison() (*expression, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	var op compareOp
	switch p.current.kind {
	case tokenEqual:
		op = compareEqual
	case tokenNotEqual:
		op = compareNotEqual
	case tokenGreater:
		op = compareGreater
	case tokenGreaterEqual:
		op = compareGreaterEqual
	case tokenLess:
		op = compareLess
	case tokenLessEqual:
		op = compareLessEqual
	default:
		return left, nil
	}
	if err := p.advance(); err != nil {
		return nil, err
	}
	right, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	return &expression{kind: expressionCompare, op: op, left: left, right: right}, nil
}

// parseUnary parses prefix "!" and "-" operators.
func (p *parser) parseUnary() (*expression, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer p.leave()

	switch p.current.kind {
	case tokenNot:
		if err := p.advance(); err != nil {
			return nil, err
		}
		operand, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &expression{kind: expressionNot, left: operand}, nil
	case tokenMinus:
		if err := p.advance(); err != nil {
			return nil, err
		}
		operand, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &expression{kind: expressionNegate, left: operand}, nil
	}
	return p.parsePrimary()
}

// parsePrimary parses a literal, variable, placeholder, quoted template or a
// parenthesized sub-expression.
func (p *parser) parsePrimary() (*expression, error) {
	switch p.current.kind {
	case tokenNumber:
		node := &expression{kind: expressionLiteral, literal: p.current.number}
		return node, p.advance()
	case tokenTrue:
		node := &expression{kind: expressionLiteral, literal: true}
		return node, p.advance()
	case tokenFalse:
		node := &expression{kind: expressionLiteral, literal: false}
		return node, p.advance()
	case tokenString:
		return p.quoted(p.current.offset, p.current.text)
	case tokenLookup:
		node := &expression{kind: expressionLookup, lookup: p.current.lookup}
		return node, p.advance()
	case tokenIdentifier:
		node := &expression{kind: expressionVariable, name: p.current.text}
		return node, p.advance()
	case tokenLeftParen:
		if err := p.advance(); err != nil {
			return nil, err
		}
		inner, err := p.parseTernary()
		if err != nil {
			return nil, err
		}
		if p.current.kind != tokenRightParen {
			return nil, compileErrorf(p.source, p.current.offset, "expected ')' in expression")
		}
		return inner, p.advance()
	}
	return nil, compileErrorf(p.source, p.current.offset, "expected a value in expression, found %s", tokenName(p.current))
}

// quoted turns a quoted expression string into a literal or, when it contains
// markers, into a compiled template. Quotes are not part of the result: a
// quoted branch in a conditional is an unquoted template.
func (p *parser) quoted(offset int, text string) (*expression, error) {
	if !hasMarkers(text) {
		return &expression{kind: expressionLiteral, literal: text}, p.advance()
	}
	compiled, err := compileTemplate(text, p.compileDepth+1)
	if err != nil {
		return nil, compileErrorf(p.source, offset, "invalid quoted template in expression: %v", err)
	}
	if compiled.Unpack() {
		return nil, compileErrorf(p.source, offset, "array unpack *${...} is not allowed inside a conditional expression")
	}
	node := &expression{kind: expressionTemplate, template: compiled}
	return node, p.advance()
}

// tokenName returns a short human readable name for a token.
func tokenName(t token) string {
	switch t.kind {
	case tokenEOF:
		return "end of expression"
	case tokenIdentifier:
		return fmt.Sprintf("variable %q", t.text)
	case tokenNumber:
		return fmt.Sprintf("number %v", t.number)
	case tokenString:
		return fmt.Sprintf("quoted string %q", t.text)
	case tokenLookup:
		return fmt.Sprintf("placeholder %q", t.lookup.path)
	case tokenNot:
		return "'!'"
	case tokenAnd:
		return "'and'"
	case tokenOr:
		return "'or'"
	case tokenTrue:
		return "'true'"
	case tokenFalse:
		return "'false'"
	case tokenLeftParen:
		return "'('"
	case tokenRightParen:
		return "')'"
	case tokenQuestion:
		return "'?'"
	case tokenColon:
		return "':'"
	case tokenEqual:
		return "'=='"
	case tokenNotEqual:
		return "'!='"
	case tokenGreater:
		return "'>'"
	case tokenGreaterEqual:
		return "'>='"
	case tokenLess:
		return "'<'"
	case tokenLessEqual:
		return "'<='"
	case tokenMinus:
		return "'-'"
	default:
		return "unknown token"
	}
}

// isIdentifierStart reports whether c may start a variable path.
func isIdentifierStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isIdentifierPart reports whether c may continue a variable path.
func isIdentifierPart(c byte) bool {
	return isIdentifierStart(c) || (c >= '0' && c <= '9') || c == '.'
}
