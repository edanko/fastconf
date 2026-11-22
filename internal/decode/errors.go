package decode

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/edanko/fastconf/internal/value"
)

// rootPath is the first segment of configuration paths in error messages. It
// mirrors the canonical document root used by the loader.
const rootPath = "config"

// pathKind identifies how a path segment is rendered in error messages.
type pathKind uint8

const (
	pathField pathKind = iota
	pathIndex
	pathMapKey
)

// pathCapacity bounds the number of recorded path segments. Configuration
// nesting is far shallower in practice; deeper paths keep their recorded
// prefix and are marked as truncated.
const pathCapacity = 32

// pathEntry is one segment of the configuration path of the value currently
// being decoded.
type pathEntry struct {
	key    string
	index  int
	parent int32
	kind   pathKind
}

// pathStack tracks the configuration path of the value being decoded.
//
// Segments live in a fixed array inside the stack value, which is only ever
// reached through a pointer for the duration of the decode call, so tracking
// the path costs no allocation. The path is rendered only when an error is
// reported.
type pathStack struct {
	nodes   [pathCapacity]pathEntry
	head    int32
	length  int32
	skipped int32
}

// newPathStack returns an empty path stack.
func newPathStack() pathStack {
	return pathStack{head: -1}
}

// pushField pushes an object key segment.
func (p *pathStack) pushField(key string) {
	p.push(pathEntry{key: key, kind: pathField})
}

// pushIndex pushes a list index segment.
func (p *pathStack) pushIndex(index int) {
	p.push(pathEntry{index: index, kind: pathIndex})
}

// pushMapKey pushes a map key segment.
func (p *pathStack) pushMapKey(key string) {
	p.push(pathEntry{key: key, kind: pathMapKey})
}

// push records one segment.
func (p *pathStack) push(entry pathEntry) {
	if p.length == pathCapacity {
		p.skipped++
		return
	}
	entry.parent = p.head
	p.nodes[p.length] = entry
	p.head = p.length
	p.length++
}

// pop removes the last segment.
func (p *pathStack) pop() {
	if p.skipped > 0 {
		p.skipped--
		return
	}
	if p.length == 0 {
		return
	}
	p.length--
	if p.length == 0 {
		p.head = -1
		return
	}
	p.head = p.nodes[p.length-1].parent
}

// String renders the current configuration path.
func (p *pathStack) String() string {
	var b strings.Builder
	b.WriteString(rootPath)
	for i := int32(0); i < p.length; i++ {
		entry := &p.nodes[i]
		switch entry.kind {
		case pathField:
			b.WriteByte('.')
			b.WriteString(entry.key)
		case pathIndex:
			b.WriteByte('[')
			b.WriteString(strconv.Itoa(entry.index))
			b.WriteByte(']')
		case pathMapKey:
			b.WriteByte('[')
			b.WriteString(entry.key)
			b.WriteByte(']')
		}
	}
	if p.skipped > 0 {
		b.WriteString("...")
	}
	return b.String()
}

// decodeError reports a decoding failure together with the configuration path
// that produced it. The path always starts at the configuration root.
type decodeError struct {
	path string
	msg  string
	err  error
}

// Error implements the error interface.
func (e *decodeError) Error() string {
	var b strings.Builder
	b.WriteString("fastconf: decode")
	if e.path != "" {
		b.WriteByte(' ')
		b.WriteString(e.path)
	}
	b.WriteString(": ")
	if e.msg != "" {
		b.WriteString(e.msg)
	} else if e.err != nil {
		b.WriteString(e.err.Error())
	}
	return b.String()
}

// Unwrap returns the underlying error, if any.
func (e *decodeError) Unwrap() error {
	return e.err
}

// errorf builds a decode error at the current configuration path.
func (p *pathStack) errorf(format string, args ...any) error {
	return &decodeError{path: p.String(), msg: fmt.Sprintf(format, args...)}
}

// errorAtField builds a decode error for a key inside the current object.
func (p *pathStack) errorAtField(key string, msg string) error {
	p.pushField(key)
	path := p.String()
	p.pop()
	return &decodeError{path: path, msg: msg}
}

// wrap attaches the configuration path to err unless err already carries one.
func (p *pathStack) wrap(err error) error {
	if err == nil {
		return nil
	}
	var existing *decodeError
	if errors.As(err, &existing) {
		return err
	}
	return &decodeError{path: p.String(), err: err}
}

// typeError reports that a value kind cannot be decoded into typ.
func (d *Decoder) typeError(path *pathStack, v value.Value, typ reflect.Type) error {
	msg := fmt.Sprintf("cannot decode %s value into %s", v.Kind(), typ)
	if !d.weak && v.IsScalar() &&
		(isScalarKind(typ.Kind()) || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array) {
		msg += " (weakly typed decoding is disabled)"
	}
	return path.errorf("%s", msg)
}
