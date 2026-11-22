package decode

import (
	"fmt"
	"reflect"
	"strings"
)

// fieldPlan describes one destination field of a struct plan: the name used in
// configuration keys and the field index path from the struct value.
type fieldPlan struct {
	name  string
	index []int
}

// fieldSlot is one entry of the open-addressed name table of a struct plan.
type fieldSlot struct {
	name  string
	hash  uint32
	field int32
	ascii bool
}

// structPlan is the cached decoding plan for one struct type.
//
// The plan stores the flattened field list, the remaining-values field (if any)
// and a hash table for case-insensitive field lookup. Building it once per type
// removes tag parsing and per-object name maps from the decode hot path.
type structPlan struct {
	typ        reflect.Type
	err        error
	fields     []fieldPlan
	slots      []fieldSlot
	mask       uint32
	remain     int32
	hasUnicode bool
}

// fieldTag holds the tag options recognized by the decoder.
type fieldTag struct {
	squash bool
	remain bool
}

// buildStructPlan computes the decoding plan for typ. The tags slice lists
// struct tag names in priority order.
func buildStructPlan(typ reflect.Type, tags []string) *structPlan {
	plan := &structPlan{typ: typ, remain: -1}
	fields, remain, err := collectFields(typ, nil, tags, map[reflect.Type]bool{typ: true})
	if err != nil {
		plan.err = err
		return plan
	}
	plan.fields = fields
	plan.remain = int32(remain)
	plan.buildTable()
	return plan
}

// collectFields flattens the decoded fields of typ. Embedded anonymous structs
// and squash-tagged fields are expanded in place, so their fields resolve
// against the parent object. prefix is the field index path of typ inside the
// destination value. remain is the index of the remaining-values field, or -1.
func collectFields(typ reflect.Type, prefix []int, tags []string, stack map[reflect.Type]bool) ([]fieldPlan, int, error) {
	fields := make([]fieldPlan, 0, typ.NumField())
	remain := -1

	for i := 0; i < typ.NumField(); i++ {
		structField := typ.Field(i)
		name, options, skip := parseFieldTag(structField, tags)
		if skip {
			continue
		}

		index := appendIndex(prefix, i)
		isPointer := structField.Type.Kind() == reflect.Pointer
		structType := structOrElemStruct(structField.Type)
		flatten := options.squash || (structField.Anonymous && name == "" && structType != nil)

		if structField.PkgPath != "" {
			// Unexported field. Promoted fields of an embedded, unexported
			// value struct are settable, but the field itself is not, so only
			// that one shape can be flattened.
			if !structField.Anonymous || isPointer || !flatten {
				continue
			}
		}

		if options.squash && options.remain {
			return nil, -1, fmt.Errorf("field %s of %s: squash and remain cannot be combined", structField.Name, typ)
		}

		if flatten {
			if structType == nil {
				return nil, -1, fmt.Errorf("field %s of %s: squash requires a struct or pointer to struct", structField.Name, typ)
			}
			if stack[structType] {
				return nil, -1, fmt.Errorf("field %s of %s: recursive struct flattening", structField.Name, typ)
			}
			stack[structType] = true
			embeddedFields, embeddedRemain, err := collectFields(structType, index, tags, stack)
			delete(stack, structType)
			if err != nil {
				return nil, -1, err
			}
			if embeddedRemain >= 0 {
				if remain >= 0 {
					return nil, -1, fmt.Errorf("type %s: multiple remain fields", typ)
				}
				remain = len(fields) + embeddedRemain
			}
			fields = append(fields, embeddedFields...)
			continue
		}

		if options.remain {
			if name != "" {
				return nil, -1, fmt.Errorf("field %s of %s: a remain field must not declare a name", structField.Name, typ)
			}
			if structField.Type.Kind() != reflect.Map || structField.Type.Key().Kind() != reflect.String {
				return nil, -1, fmt.Errorf("field %s of %s: remain requires a map with string keys", structField.Name, typ)
			}
			if remain >= 0 {
				return nil, -1, fmt.Errorf("type %s: multiple remain fields", typ)
			}
			remain = len(fields)
			fields = append(fields, fieldPlan{index: index})
			continue
		}

		if name == "" {
			name = structField.Name
		}
		fields = append(fields, fieldPlan{name: name, index: index})
	}

	return fields, remain, nil
}

// parseFieldTag resolves the configuration name and tag options of a field.
// The first non-empty name among the tags in priority order wins; options are
// collected from every tag. A first tag of exactly "-" skips the field.
func parseFieldTag(structField reflect.StructField, tags []string) (string, fieldTag, bool) {
	var options fieldTag
	var name string
	first := ""
	seen := false

	for _, tagName := range tags {
		raw, ok := structField.Tag.Lookup(tagName)
		if !ok {
			continue
		}
		if !seen {
			first, seen = raw, true
		}
		base := raw
		hasComma := false
		if comma := strings.IndexByte(raw, ','); comma >= 0 {
			base, hasComma = raw[:comma], true
			for _, option := range strings.Split(raw[comma+1:], ",") {
				switch option {
				case "squash":
					options.squash = true
				case "remain":
					options.remain = true
				}
			}
		}
		if name == "" && base != "" && (base != "-" || hasComma) {
			name = base
		}
	}

	if seen && first == "-" {
		return "", fieldTag{}, true
	}
	return name, options, false
}

// structOrElemStruct returns the struct type behind t, dereferencing one level
// of pointer, or nil when t is not a struct or pointer to struct.
func structOrElemStruct(t reflect.Type) reflect.Type {
	switch t.Kind() {
	case reflect.Struct:
		return t
	case reflect.Pointer:
		if t.Elem().Kind() == reflect.Struct {
			return t.Elem()
		}
	}
	return nil
}

// appendIndex returns a new field index path ending with field.
func appendIndex(prefix []int, field int) []int {
	index := make([]int, len(prefix)+1)
	copy(index, prefix)
	index[len(prefix)] = field
	return index
}

// buildTable fills the name lookup table of the plan. The first declaration of
// a name wins over later duplicates.
func (p *structPlan) buildTable() {
	if len(p.fields) == 0 {
		return
	}
	size := 8
	for size < len(p.fields)*2 {
		size <<= 1
	}
	p.slots = make([]fieldSlot, size)
	for i := range p.slots {
		p.slots[i].field = -1
	}
	p.mask = uint32(size - 1)

	for i := range p.fields {
		name := p.fields[i].name
		if name == "" {
			continue
		}
		hash, ascii := foldHash(name)
		if !ascii {
			p.hasUnicode = true
		}
		slot := hash & p.mask
		duplicate := false
		for {
			current := &p.slots[slot]
			if current.field < 0 {
				break
			}
			if current.hash == hash && strings.EqualFold(current.name, name) {
				duplicate = true
				break
			}
			slot = (slot + 1) & p.mask
		}
		if duplicate {
			continue
		}
		p.slots[slot] = fieldSlot{name: name, hash: hash, field: int32(i), ascii: ascii}
	}
}

// lookup resolves a configuration key to a field index, matching field names
// case-insensitively.
func (p *structPlan) lookup(key string) (int32, bool) {
	if len(p.slots) == 0 {
		return -1, false
	}
	hash, ascii := foldHash(key)
	slot := hash & p.mask
	for {
		current := &p.slots[slot]
		if current.field < 0 {
			break
		}
		if current.hash == hash {
			if current.name == key {
				return current.field, true
			}
			switch {
			case ascii && current.ascii && equalFoldASCII(current.name, key):
				return current.field, true
			case !ascii || !current.ascii:
				if strings.EqualFold(current.name, key) {
					return current.field, true
				}
			}
		}
		slot = (slot + 1) & p.mask
	}

	// Unicode case folding cannot be folded into the ASCII hash, so fall back
	// to a full scan when either side contains non-ASCII bytes.
	if !ascii || p.hasUnicode {
		for i := range p.fields {
			if p.fields[i].name == "" {
				continue
			}
			if strings.EqualFold(p.fields[i].name, key) {
				return int32(i), true
			}
		}
	}
	return -1, false
}

// foldHash hashes s with ASCII case folding. It also reports whether s is pure
// ASCII.
func foldHash(s string) (uint32, bool) {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	hash := uint32(offset32)
	ascii := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			c += 'a' - 'A'
		case c >= 0x80:
			ascii = false
		}
		hash ^= uint32(c)
		hash *= prime32
	}
	return hash, ascii
}

// equalFoldASCII reports whether a and b are equal under ASCII case folding.
// Both arguments must be pure ASCII.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca != cb && lowerASCII(ca) != lowerASCII(cb) {
			return false
		}
	}
	return true
}

// lowerASCII lowercases one ASCII byte.
func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}
