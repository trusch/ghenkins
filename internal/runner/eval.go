package runner

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Evaluator is a GitHub Actions expression evaluator.
type Evaluator struct{}

var interpolateRe = regexp.MustCompile(`\$\{\{(.*?)\}\}`)

// Interpolate replaces all ${{ expr }} occurrences in s with their evaluated string values.
func (e *Evaluator) Interpolate(s string, ctx *EvalContext) string {
	return interpolateRe.ReplaceAllStringFunc(s, func(match string) string {
		inner := strings.TrimSpace(match[3 : len(match)-2])
		val, err := e.Eval(inner, ctx)
		if err != nil {
			return ""
		}
		return valueToString(val)
	})
}

// EvalBool evaluates s as a boolean expression (for if: fields).
// Strips ${{ }} wrapper if present. Returns true for non-empty, non-false, non-zero values.
func (e *Evaluator) EvalBool(s string, ctx *EvalContext) (bool, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "${{") && strings.HasSuffix(s, "}}") {
		s = strings.TrimSpace(s[3 : len(s)-2])
	}
	val, err := e.Eval(s, ctx)
	if err != nil {
		return false, err
	}
	return isTruthy(val), nil
}

// Eval evaluates a single bare expression string and returns the result.
func (e *Evaluator) Eval(expr string, ctx *EvalContext) (interface{}, error) {
	toks := tokenize(expr)
	p := &exprParser{tokens: toks, ctx: ctx}
	val, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.tokens) && p.tokens[p.pos].typ != tokEOF {
		return nil, fmt.Errorf("unexpected token: %q", p.tokens[p.pos].val)
	}
	return val, nil
}

// ---- Tokenizer ----

type tokType int

const (
	tokString tokType = iota
	tokNumber
	tokBool
	tokNull
	tokIdent
	tokLParen
	tokRParen
	tokComma
	tokDot
	tokLBracket
	tokRBracket
	tokEQ
	tokNEQ
	tokLT
	tokLTE
	tokGT
	tokGTE
	tokAND
	tokOR
	tokNOT
	tokEOF
)

type token struct {
	typ tokType
	val string
}

func tokenize(s string) []token {
	var tokens []token
	i := 0
	for i < len(s) {
		ch := s[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			i++
		case ch == '\'':
			j := i + 1
			for j < len(s) && s[j] != '\'' {
				j++
			}
			tokens = append(tokens, token{tokString, s[i+1 : j]})
			i = j + 1
		case (ch >= '0' && ch <= '9') || (ch == '-' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9'):
			j := i
			if s[j] == '-' {
				j++
			}
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			tokens = append(tokens, token{tokNumber, s[i:j]})
			i = j
		case ch == '(':
			tokens = append(tokens, token{tokLParen, "("})
			i++
		case ch == ')':
			tokens = append(tokens, token{tokRParen, ")"})
			i++
		case ch == ',':
			tokens = append(tokens, token{tokComma, ","})
			i++
		case ch == '.':
			tokens = append(tokens, token{tokDot, "."})
			i++
		case ch == '[':
			tokens = append(tokens, token{tokLBracket, "["})
			i++
		case ch == ']':
			tokens = append(tokens, token{tokRBracket, "]"})
			i++
		case ch == '=' && i+1 < len(s) && s[i+1] == '=':
			tokens = append(tokens, token{tokEQ, "=="})
			i += 2
		case ch == '!' && i+1 < len(s) && s[i+1] == '=':
			tokens = append(tokens, token{tokNEQ, "!="})
			i += 2
		case ch == '<' && i+1 < len(s) && s[i+1] == '=':
			tokens = append(tokens, token{tokLTE, "<="})
			i += 2
		case ch == '<':
			tokens = append(tokens, token{tokLT, "<"})
			i++
		case ch == '>' && i+1 < len(s) && s[i+1] == '=':
			tokens = append(tokens, token{tokGTE, ">="})
			i += 2
		case ch == '>':
			tokens = append(tokens, token{tokGT, ">"})
			i++
		case ch == '&' && i+1 < len(s) && s[i+1] == '&':
			tokens = append(tokens, token{tokAND, "&&"})
			i += 2
		case ch == '|' && i+1 < len(s) && s[i+1] == '|':
			tokens = append(tokens, token{tokOR, "||"})
			i += 2
		case ch == '!':
			tokens = append(tokens, token{tokNOT, "!"})
			i++
		case isIdentStart(ch):
			j := i
			for j < len(s) && isIdentCont(s[j]) {
				j++
			}
			word := s[i:j]
			switch word {
			case "true":
				tokens = append(tokens, token{tokBool, "true"})
			case "false":
				tokens = append(tokens, token{tokBool, "false"})
			case "null":
				tokens = append(tokens, token{tokNull, "null"})
			default:
				tokens = append(tokens, token{tokIdent, word})
			}
			i = j
		default:
			i++
		}
	}
	tokens = append(tokens, token{tokEOF, ""})
	return tokens
}

func isIdentStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

func isIdentCont(ch byte) bool {
	return isIdentStart(ch) || (ch >= '0' && ch <= '9') || ch == '-'
}

// ---- Parser ----

type exprParser struct {
	tokens []token
	pos    int
	ctx    *EvalContext
}

func (p *exprParser) peek() token {
	if p.pos >= len(p.tokens) {
		return token{tokEOF, ""}
	}
	return p.tokens[p.pos]
}

func (p *exprParser) consume() token {
	t := p.peek()
	p.pos++
	return t
}

func (p *exprParser) expect(typ tokType) (token, error) {
	t := p.peek()
	if t.typ != typ {
		return token{}, fmt.Errorf("expected token type %d, got %d (%q)", typ, t.typ, t.val)
	}
	p.pos++
	return t, nil
}

func (p *exprParser) parseExpr() (interface{}, error) {
	return p.parseOr()
}

func (p *exprParser) parseOr() (interface{}, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().typ == tokOR {
		p.consume()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = isTruthy(left) || isTruthy(right)
	}
	return left, nil
}

func (p *exprParser) parseAnd() (interface{}, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek().typ == tokAND {
		p.consume()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = isTruthy(left) && isTruthy(right)
	}
	return left, nil
}

func (p *exprParser) parseNot() (interface{}, error) {
	if p.peek().typ == tokNOT {
		p.consume()
		val, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return !isTruthy(val), nil
	}
	return p.parseComparison()
}

func (p *exprParser) parseComparison() (interface{}, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	switch p.peek().typ {
	case tokEQ, tokNEQ, tokLT, tokLTE, tokGT, tokGTE:
		op := p.consume()
		right, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		return evalComparison(left, op.typ, right)
	}
	return left, nil
}

func (p *exprParser) parsePrimary() (interface{}, error) {
	t := p.peek()
	switch t.typ {
	case tokLParen:
		p.consume()
		val, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tokRParen); err != nil {
			return nil, err
		}
		return val, nil
	case tokString:
		p.consume()
		return t.val, nil
	case tokNumber:
		p.consume()
		f, err := strconv.ParseFloat(t.val, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number: %s", t.val)
		}
		return f, nil
	case tokBool:
		p.consume()
		return t.val == "true", nil
	case tokNull:
		p.consume()
		return nil, nil
	case tokIdent:
		return p.parseIdentOrCall()
	}
	return nil, fmt.Errorf("unexpected token: %q", t.val)
}

func (p *exprParser) parseIdentOrCall() (interface{}, error) {
	name := p.consume().val

	// Function call
	if p.peek().typ == tokLParen {
		p.consume()
		var args []interface{}
		for p.peek().typ != tokRParen && p.peek().typ != tokEOF {
			if len(args) > 0 {
				if _, err := p.expect(tokComma); err != nil {
					return nil, err
				}
			}
			arg, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
		}
		if _, err := p.expect(tokRParen); err != nil {
			return nil, err
		}
		return p.callFunction(name, args)
	}

	// Property access chain
	parts := []string{name}
loop:
	for {
		switch p.peek().typ {
		case tokDot:
			p.consume()
			t, err := p.expect(tokIdent)
			if err != nil {
				return nil, err
			}
			parts = append(parts, t.val)
		case tokLBracket:
			p.consume()
			t, err := p.expect(tokNumber)
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(tokRBracket); err != nil {
				return nil, err
			}
			parts = append(parts, "["+t.val+"]")
		default:
			break loop
		}
	}

	val, _ := p.ctx.Lookup(parts)
	return val, nil
}

func (p *exprParser) callFunction(name string, args []interface{}) (interface{}, error) {
	switch name {
	case "contains":
		if len(args) != 2 {
			return nil, fmt.Errorf("contains: expected 2 args, got %d", len(args))
		}
		return evalContains(args[0], args[1])
	case "startsWith":
		if len(args) != 2 {
			return nil, fmt.Errorf("startsWith: expected 2 args, got %d", len(args))
		}
		return strings.HasPrefix(strings.ToLower(valueToString(args[0])), strings.ToLower(valueToString(args[1]))), nil
	case "endsWith":
		if len(args) != 2 {
			return nil, fmt.Errorf("endsWith: expected 2 args, got %d", len(args))
		}
		return strings.HasSuffix(strings.ToLower(valueToString(args[0])), strings.ToLower(valueToString(args[1]))), nil
	case "format":
		if len(args) < 1 {
			return nil, fmt.Errorf("format: expected at least 1 arg")
		}
		return evalFormat(args)
	case "join":
		if len(args) < 1 || len(args) > 2 {
			return nil, fmt.Errorf("join: expected 1 or 2 args, got %d", len(args))
		}
		sep := ","
		if len(args) == 2 {
			sep = valueToString(args[1])
		}
		return evalJoin(args[0], sep)
	case "toJSON":
		if len(args) != 1 {
			return nil, fmt.Errorf("toJSON: expected 1 arg, got %d", len(args))
		}
		b, err := json.Marshal(args[0])
		if err != nil {
			return nil, err
		}
		return string(b), nil
	case "fromJSON":
		if len(args) != 1 {
			return nil, fmt.Errorf("fromJSON: expected 1 arg, got %d", len(args))
		}
		var v interface{}
		if err := json.Unmarshal([]byte(valueToString(args[0])), &v); err != nil {
			return nil, err
		}
		return v, nil
	case "always":
		return true, nil
	case "success":
		return p.ctx.JobStatus == JobStatusSuccess || p.ctx.JobStatus == JobStatusPending, nil
	case "failure":
		return p.ctx.JobStatus == JobStatusFailure, nil
	case "cancelled":
		return p.ctx.Cancelled, nil
	case "hashFiles":
		return "", nil // MVP stub
	}
	return nil, fmt.Errorf("unknown function: %s", name)
}

// ---- Built-in helpers ----

func evalContains(search, item interface{}) (interface{}, error) {
	switch s := search.(type) {
	case string:
		return strings.Contains(strings.ToLower(s), strings.ToLower(valueToString(item))), nil
	case []interface{}:
		for _, v := range s {
			if valuesEqual(v, item) {
				return true, nil
			}
		}
		return false, nil
	}
	return strings.Contains(strings.ToLower(valueToString(search)), strings.ToLower(valueToString(item))), nil
}

func evalFormat(args []interface{}) (interface{}, error) {
	tmpl := valueToString(args[0])
	for i, arg := range args[1:] {
		tmpl = strings.ReplaceAll(tmpl, fmt.Sprintf("{%d}", i), valueToString(arg))
	}
	return tmpl, nil
}

func evalJoin(arr interface{}, sep string) (interface{}, error) {
	switch v := arr.(type) {
	case []interface{}:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = valueToString(item)
		}
		return strings.Join(parts, sep), nil
	case string:
		return v, nil
	}
	return valueToString(arr), nil
}

func evalComparison(left interface{}, op tokType, right interface{}) (interface{}, error) {
	switch op {
	case tokEQ:
		return valuesEqual(left, right), nil
	case tokNEQ:
		return !valuesEqual(left, right), nil
	}
	// Ordered comparisons: prefer numeric, fall back to string
	lf, lok := toFloat(left)
	rf, rok := toFloat(right)
	if lok && rok {
		switch op {
		case tokLT:
			return lf < rf, nil
		case tokLTE:
			return lf <= rf, nil
		case tokGT:
			return lf > rf, nil
		case tokGTE:
			return lf >= rf, nil
		}
	}
	cmp := strings.Compare(strings.ToLower(valueToString(left)), strings.ToLower(valueToString(right)))
	switch op {
	case tokLT:
		return cmp < 0, nil
	case tokLTE:
		return cmp <= 0, nil
	case tokGT:
		return cmp > 0, nil
	case tokGTE:
		return cmp >= 0, nil
	}
	return nil, fmt.Errorf("unknown comparison op: %v", op)
}

func valuesEqual(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if aok && bok {
		return af == bf
	}
	// Case-insensitive string comparison per GitHub Actions spec
	return strings.EqualFold(valueToString(a), valueToString(b))
}

// ---- Type utilities ----

func isTruthy(v interface{}) bool {
	if v == nil {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case float64:
		return val != 0
	case int:
		return val != 0
	case int64:
		return val != 0
	case string:
		return val != ""
	}
	return true
}

func valueToString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case float64:
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func toFloat(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case string:
		f, err := strconv.ParseFloat(val, 64)
		if err == nil {
			return f, true
		}
	}
	return 0, false
}
