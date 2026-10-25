package game

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"strconv"
	"strings"
)

type ruleKind uint8

const (
	ruleBool ruleKind = iota
	ruleInt
	ruleString
)

type ruleValue struct {
	kind    ruleKind
	boolean bool
	number  int64
	text    string
}

func boolValue(v bool) ruleValue     { return ruleValue{kind: ruleBool, boolean: v} }
func intValue(v int64) ruleValue     { return ruleValue{kind: ruleInt, number: v} }
func stringValue(v string) ruleValue { return ruleValue{kind: ruleString, text: v} }

type ruleEnvironment struct {
	context Context
	effects *Effects
}
type ruleExpression struct {
	kind     ruleKind
	run      func(*ruleEnvironment) (ruleValue, error)
	constant *ruleValue
}

func literalExpression(v ruleValue) ruleExpression {
	return ruleExpression{kind: v.kind, constant: &v, run: func(*ruleEnvironment) (ruleValue, error) { return v, nil }}
}

// Expressions are parsed, type checked and compiled once. There is no eval,
// reflection, dynamic function call, loop, clock, random source or external I/O.
func compileRuleExpression(source string, options map[string]ruleValue, optionsOnly bool) (ruleExpression, error) {
	if len(source) == 0 || len(source) > 1024 {
		return ruleExpression{}, fmt.Errorf("expression must contain 1–1024 bytes")
	}
	node, err := parser.ParseExpr(source)
	if err != nil {
		return ruleExpression{}, fmt.Errorf("invalid expression syntax")
	}
	depth, count, exceeded := 0, 0, false
	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil {
			depth--
			return true
		}
		depth++
		count++
		if depth > 24 || count > 256 {
			exceeded = true
		}
		return true
	})
	if exceeded {
		return ruleExpression{}, fmt.Errorf("expression exceeds 256 nodes or 24 levels")
	}
	var build func(ast.Expr) (ruleExpression, error)
	build = func(n ast.Expr) (ruleExpression, error) {
		switch n := n.(type) {
		case *ast.ParenExpr:
			return build(n.X)
		case *ast.Ident:
			if n.Name == "true" {
				return literalExpression(boolValue(true)), nil
			}
			if n.Name == "false" {
				return literalExpression(boolValue(false)), nil
			}
			return resolveRuleField(n.Name, options, optionsOnly)
		case *ast.SelectorExpr:
			name, ok := ruleSelector(n)
			if !ok {
				return ruleExpression{}, fmt.Errorf("invalid field reference")
			}
			return resolveRuleField(name, options, optionsOnly)
		case *ast.BasicLit:
			switch n.Kind {
			case token.INT:
				v, err := strconv.ParseInt(n.Value, 10, 64)
				if err != nil {
					return ruleExpression{}, fmt.Errorf("integer must be a signed decimal value")
				}
				return literalExpression(intValue(v)), nil
			case token.STRING:
				v, err := strconv.Unquote(n.Value)
				if err != nil {
					return ruleExpression{}, err
				}
				return literalExpression(stringValue(v)), nil
			}
		case *ast.UnaryExpr:
			x, err := build(n.X)
			if err != nil {
				return ruleExpression{}, err
			}
			if (n.Op != token.NOT || x.kind != ruleBool) && (n.Op != token.SUB || x.kind != ruleInt) {
				return ruleExpression{}, fmt.Errorf("invalid unary operation")
			}
			expr := ruleExpression{kind: x.kind, run: func(env *ruleEnvironment) (ruleValue, error) {
				v, err := x.run(env)
				if err != nil {
					return ruleValue{}, err
				}
				if n.Op == token.NOT {
					return boolValue(!v.boolean), nil
				}
				if v.number == math.MinInt64 {
					return ruleValue{}, fmt.Errorf("integer overflow")
				}
				return intValue(-v.number), nil
			}}
			return foldRuleExpression(expr, x.constant != nil)
		case *ast.BinaryExpr:
			left, err := build(n.X)
			if err != nil {
				return ruleExpression{}, err
			}
			right, err := build(n.Y)
			if err != nil {
				return ruleExpression{}, err
			}
			kind, err := binaryRuleKind(n.Op, left.kind, right.kind)
			if err != nil {
				return ruleExpression{}, err
			}
			if (n.Op == token.QUO || n.Op == token.REM) && right.constant != nil && right.constant.number == 0 {
				return ruleExpression{}, fmt.Errorf("division by zero")
			}
			expr := ruleExpression{kind: kind, run: func(env *ruleEnvironment) (ruleValue, error) {
				a, err := left.run(env)
				if err != nil {
					return ruleValue{}, err
				}
				if n.Op == token.LAND && !a.boolean {
					return boolValue(false), nil
				}
				if n.Op == token.LOR && a.boolean {
					return boolValue(true), nil
				}
				b, err := right.run(env)
				if err != nil {
					return ruleValue{}, err
				}
				return binaryRuleValue(n.Op, a, b)
			}}
			return foldRuleExpression(expr, left.constant != nil && right.constant != nil)
		}
		return ruleExpression{}, fmt.Errorf("unsupported expression construct")
	}
	return build(node)
}
func ruleSelector(n ast.Expr) (string, bool) {
	switch n := n.(type) {
	case *ast.Ident:
		return n.Name, true
	case *ast.SelectorExpr:
		prefix, ok := ruleSelector(n.X)
		return prefix + "." + n.Sel.Name, ok
	}
	return "", false
}
func foldRuleExpression(expr ruleExpression, constant bool) (ruleExpression, error) {
	if !constant {
		return expr, nil
	}
	v, err := expr.run(nil)
	if err != nil {
		return ruleExpression{}, err
	}
	return literalExpression(v), nil
}
func binaryRuleKind(op token.Token, a, b ruleKind) (ruleKind, error) {
	if a != b {
		return 0, fmt.Errorf("expression operand types differ")
	}
	switch op {
	case token.LAND, token.LOR:
		if a == ruleBool {
			return ruleBool, nil
		}
	case token.EQL, token.NEQ:
		return ruleBool, nil
	case token.LSS, token.GTR, token.LEQ, token.GEQ:
		if a == ruleInt {
			return ruleBool, nil
		}
	case token.ADD, token.SUB, token.MUL, token.QUO, token.REM:
		if a == ruleInt {
			return ruleInt, nil
		}
	}
	return 0, fmt.Errorf("operator does not support these operand types")
}
func binaryRuleValue(op token.Token, a, b ruleValue) (ruleValue, error) {
	switch op {
	case token.LAND:
		return boolValue(a.boolean && b.boolean), nil
	case token.LOR:
		return boolValue(a.boolean || b.boolean), nil
	case token.EQL:
		return boolValue(a == b), nil
	case token.NEQ:
		return boolValue(a != b), nil
	case token.LSS:
		return boolValue(a.number < b.number), nil
	case token.GTR:
		return boolValue(a.number > b.number), nil
	case token.LEQ:
		return boolValue(a.number <= b.number), nil
	case token.GEQ:
		return boolValue(a.number >= b.number), nil
	}
	x, y := a.number, b.number
	switch op {
	case token.ADD:
		if (y > 0 && x > math.MaxInt64-y) || (y < 0 && x < math.MinInt64-y) {
			break
		}
		return intValue(x + y), nil
	case token.SUB:
		if (y > 0 && x < math.MinInt64+y) || (y < 0 && x > math.MaxInt64+y) {
			break
		}
		return intValue(x - y), nil
	case token.MUL:
		if x == 0 || y == 0 {
			return intValue(0), nil
		}
		if (x == math.MinInt64 && y == -1) || (y == math.MinInt64 && x == -1) {
			break
		}
		result := x * y
		if result/y != x {
			break
		}
		return intValue(result), nil
	case token.QUO, token.REM:
		if y == 0 {
			return ruleValue{}, fmt.Errorf("division by zero")
		}
		if x == math.MinInt64 && y == -1 {
			break
		}
		if op == token.QUO {
			return intValue(x / y), nil
		}
		return intValue(x % y), nil
	}
	return ruleValue{}, fmt.Errorf("integer overflow")
}
func resolveRuleField(name string, options map[string]ruleValue, optionsOnly bool) (ruleExpression, error) {
	if key, ok := strings.CutPrefix(name, "options."); ok {
		if v, ok := options[key]; ok {
			return literalExpression(v), nil
		}
		return ruleExpression{}, fmt.Errorf("unknown option %q", key)
	}
	if !optionsOnly {
		if key, ok := strings.CutPrefix(name, "effects."); ok {
			if field, ok := ruleEffectFields[key]; ok {
				return ruleExpression{kind: field.kind, run: func(env *ruleEnvironment) (ruleValue, error) { return field.read(env.effects), nil }}, nil
			}
		}
		if field, ok := ruleContextFields[name]; ok {
			return ruleExpression{kind: field.kind, run: func(env *ruleEnvironment) (ruleValue, error) { return field.read(env.context), nil }}, nil
		}
	}
	return ruleExpression{}, fmt.Errorf("unknown or unavailable field %q", name)
}
