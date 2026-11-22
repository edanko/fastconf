package expr

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// evalContext carries the scope and the chain of placeholder lookups that are
// currently being resolved. The chain is used for cycle detection.
type evalContext struct {
	scope  Scope
	frames []lookupFrame
}

// lookupFrame is one entry of the placeholder resolution chain.
type lookupFrame struct {
	key  string
	path string
}

// eval evaluates a compiled template.
func (t *Template) eval(ctx *evalContext) (any, error) {
	if t.unpack {
		return ctx.evaluateLookup(t.fragments[0].lookup)
	}
	if len(t.fragments) == 0 {
		return t.source, nil
	}
	if len(t.fragments) == 1 {
		return t.evalFragment(ctx, &t.fragments[0])
	}

	var builder strings.Builder
	builder.Grow(len(t.source))
	for i := range t.fragments {
		fragment := &t.fragments[i]
		if fragment.kind == fragmentLiteral {
			builder.WriteString(fragment.text)
			continue
		}
		value, err := t.evalFragment(ctx, fragment)
		if err != nil {
			return nil, err
		}
		text, err := formatValue(value)
		if err != nil {
			return nil, err
		}
		builder.WriteString(text)
	}
	return builder.String(), nil
}

// evalFragment evaluates one fragment and returns its typed value.
func (t *Template) evalFragment(ctx *evalContext, fragment *fragment) (any, error) {
	switch fragment.kind {
	case fragmentLiteral:
		return fragment.text, nil
	case fragmentLookup:
		return ctx.evaluateLookup(fragment.lookup)
	case fragmentExpression:
		return ctx.evaluateExpression(fragment.expr)
	default:
		return nil, errors.New("expr: unknown template fragment")
	}
}

// evaluateLookup resolves one ${path} placeholder, applying its default when
// the path is not defined.
func (ctx *evalContext) evaluateLookup(l lookup) (any, error) {
	value, err := ctx.lookupPath(l.path)
	if err != nil {
		var missing *notFoundError
		if l.present && errors.As(err, &missing) {
			return cloneDefault(l.def), nil
		}
		return nil, err
	}
	return value, nil
}

// lookupPath looks path up in the scope and recursively resolves a referenced
// string that itself contains placeholders.
func (ctx *evalContext) lookupPath(path string) (any, error) {
	key := strings.ToLower(path)
	if len(ctx.frames) >= maxLookupDepth {
		return nil, fmt.Errorf("placeholder %q is nested more than %d lookups deep", path, maxLookupDepth)
	}
	for i := range ctx.frames {
		if ctx.frames[i].key == key {
			chain := make([]string, 0, len(ctx.frames)-i+1)
			for _, frame := range ctx.frames[i:] {
				chain = append(chain, frame.path)
			}
			chain = append(chain, path)
			return nil, &CycleError{Chain: chain}
		}
	}

	value, ok := ctx.scope.Lookup(path)
	if !ok {
		return nil, &notFoundError{path: path}
	}
	source, isString := value.(string)
	if !isString || !hasMarkers(source) {
		return value, nil
	}

	nested, err := Compile(source)
	if err != nil {
		return nil, err
	}
	ctx.frames = append(ctx.frames, lookupFrame{key: key, path: path})
	resolved, err := nested.eval(ctx)
	ctx.frames = ctx.frames[:len(ctx.frames)-1]
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

// evaluateExpression evaluates one conditional expression node.
func (ctx *evalContext) evaluateExpression(node *expression) (any, error) {
	switch node.kind {
	case expressionLiteral:
		return cloneDefault(node.literal), nil
	case expressionVariable:
		return ctx.evaluateLookup(lookup{path: node.name})
	case expressionLookup:
		return ctx.evaluateLookup(node.lookup)
	case expressionTemplate:
		return node.template.eval(ctx)
	case expressionNot:
		value, err := ctx.evaluateExpression(node.left)
		if err != nil {
			return nil, err
		}
		return !truthy(value), nil
	case expressionNegate:
		value, err := ctx.evaluateExpression(node.left)
		if err != nil {
			return nil, err
		}
		number, ok := numberValue(value)
		if !ok {
			return nil, fmt.Errorf("cannot negate a value of type %T", value)
		}
		return -number, nil
	case expressionAnd:
		left, err := ctx.evaluateExpression(node.left)
		if err != nil {
			return nil, err
		}
		if !truthy(left) {
			return false, nil
		}
		right, err := ctx.evaluateExpression(node.right)
		if err != nil {
			return nil, err
		}
		return truthy(right), nil
	case expressionOr:
		left, err := ctx.evaluateExpression(node.left)
		if err != nil {
			return nil, err
		}
		if truthy(left) {
			return true, nil
		}
		right, err := ctx.evaluateExpression(node.right)
		if err != nil {
			return nil, err
		}
		return truthy(right), nil
	case expressionCompare:
		return ctx.evaluateComparison(node)
	case expressionTernary:
		condition, err := ctx.evaluateExpression(node.cond)
		if err != nil {
			return nil, err
		}
		if truthy(condition) {
			return ctx.evaluateExpression(node.left)
		}
		return ctx.evaluateExpression(node.right)
	default:
		return nil, errors.New("expr: unknown expression node")
	}
}

// evaluateComparison evaluates one comparison operator.
func (ctx *evalContext) evaluateComparison(node *expression) (any, error) {
	left, err := ctx.evaluateExpression(node.left)
	if err != nil {
		return nil, err
	}
	right, err := ctx.evaluateExpression(node.right)
	if err != nil {
		return nil, err
	}

	switch node.op {
	case compareEqual:
		return valuesEqual(left, right), nil
	case compareNotEqual:
		return !valuesEqual(left, right), nil
	}

	ordering, err := compareOrdered(left, right)
	if err != nil {
		return nil, err
	}
	switch node.op {
	case compareGreater:
		return ordering > 0, nil
	case compareGreaterEqual:
		return ordering >= 0, nil
	case compareLess:
		return ordering < 0, nil
	case compareLessEqual:
		return ordering <= 0, nil
	default:
		return nil, errors.New("expr: unknown comparison operator")
	}
}

// cloneDefault returns a copy of a default value so callers cannot mutate a
// list that is shared by a compiled template.
func cloneDefault(value any) any {
	items, ok := value.([]any)
	if !ok {
		return value
	}
	clone := make([]any, len(items))
	copy(clone, items)
	return clone
}

// formatValue renders a resolved value as text for template concatenation.
func formatValue(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	case []any, map[string]any:
		return "", fmt.Errorf("cannot interpolate a value of type %T into a text fragment; use the placeholder alone or unpack a list with *${...}", value)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32), nil
	default:
		return fmt.Sprint(value), nil
	}
}

// numberValue converts any Go numeric value to float64.
func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case uintptr:
		return float64(typed), true
	default:
		return 0, false
	}
}

// truthy converts a value to a boolean condition. Booleans keep their value;
// nil, zero numbers, empty strings and empty collections are false.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	}
	if number, ok := numberValue(value); ok {
		return number != 0
	}
	return true
}

// valuesEqual reports whether two values are equal. Numbers compare numerically
// across Go integer and float types; other scalars compare by type and value;
// composite values compare deeply.
func valuesEqual(left, right any) bool {
	if leftNumber, ok := numberValue(left); ok {
		rightNumber, rightIsNumber := numberValue(right)
		return rightIsNumber && leftNumber == rightNumber
	}
	switch leftTyped := left.(type) {
	case nil:
		return right == nil
	case string:
		rightTyped, ok := right.(string)
		return ok && leftTyped == rightTyped
	case bool:
		rightTyped, ok := right.(bool)
		return ok && leftTyped == rightTyped
	}
	return reflect.DeepEqual(left, right)
}

// compareOrdered orders two values for the relational comparison operators.
// Only numbers and strings are ordered.
func compareOrdered(left, right any) (int, error) {
	if leftNumber, ok := numberValue(left); ok {
		rightNumber, rightIsNumber := numberValue(right)
		if !rightIsNumber {
			return 0, fmt.Errorf("cannot order %T and %T", left, right)
		}
		switch {
		case leftNumber < rightNumber:
			return -1, nil
		case leftNumber > rightNumber:
			return 1, nil
		default:
			return 0, nil
		}
	}
	if leftString, ok := left.(string); ok {
		rightString, rightIsString := right.(string)
		if !rightIsString {
			return 0, fmt.Errorf("cannot order %T and %T", left, right)
		}
		return strings.Compare(leftString, rightString), nil
	}
	return 0, fmt.Errorf("cannot order %T and %T", left, right)
}
