// Package decode converts an immutable configuration value tree into Go
// values.
//
// Decoding is plan driven. For every struct type the decoder builds a plan once
// and caches it in a sync.Map: field names, tag options, flattened embedded
// fields and the remaining-values field are all resolved at that point. The hot
// path therefore does not parse tags, build name maps per object or allocate
// per field, and the configuration path needed for errors is tracked with stack
// values.
//
// Values are matched to struct fields by name, case-insensitively. Field names
// come from the configured tag name, then the json, yaml and toml tags, and
// finally the Go field name. The squash and remain tag options flatten a struct
// into its parent object and collect unmatched keys respectively.
package decode

import (
	"fmt"
	"math"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/edanko/fastconf/hook"
	"github.com/edanko/fastconf/internal/value"
)

// Config controls decoding behaviour.
type Config struct {
	// WeaklyTyped enables conversions between scalar configuration types, for
	// example the string "42" into an int field or an int into a string field.
	WeaklyTyped bool
	// Strict makes an unknown object key a decoding error.
	Strict bool
	// TagName is an extra struct tag name consulted before the built-in names.
	TagName string
	// Hooks are consulted for values that cannot be assigned directly.
	Hooks []hook.Func
}

// Decoder decodes configuration values into Go structs.
//
// A Decoder is safe for concurrent use. Plans are cached per reflect.Type, so a
// Decoder should be created once and reused.
type Decoder struct {
	weak      bool
	strict    bool
	tags      []string
	hooks     hook.Func
	hasHooks  bool
	plans     sync.Map // map[reflect.Type]*structPlan
	recentA   atomic.Pointer[structPlan]
	recentB   atomic.Pointer[structPlan]
	recentSeq atomic.Uint64
}

// New returns a decoder with the given configuration.
func New(cfg Config) *Decoder {
	hooks := append([]hook.Func(nil), cfg.Hooks...)
	hasHooks := false
	for _, fn := range hooks {
		if fn != nil {
			hasHooks = true
			break
		}
	}
	return &Decoder{
		weak:     cfg.WeaklyTyped,
		strict:   cfg.Strict,
		tags:     tagNames(cfg.TagName),
		hooks:    hook.Compose(hooks...),
		hasHooks: hasHooks,
	}
}

// tagNames returns the struct tag names to consult in priority order.
func tagNames(primary string) []string {
	names := make([]string, 0, 4)
	if primary != "" {
		names = append(names, primary)
	}
	for _, name := range [...]string{"mapstructure", "json", "yaml", "toml"} {
		if name != primary {
			names = append(names, name)
		}
	}
	return names
}

// Decode writes src into the value pointed to by dst.
//
// dst must be a non-nil pointer. A nil src is treated as a null value and
// zeroes dst. Values that are absent from src keep their current field value;
// maps and slices are replaced rather than updated.
func (d *Decoder) Decode(src *value.Value, dst any) error {
	target := reflect.ValueOf(dst)
	if !target.IsValid() || target.Kind() != reflect.Pointer || target.IsNil() {
		return &decodeError{path: rootPath, msg: "destination must be a non-nil pointer"}
	}
	if src == nil {
		target.Elem().SetZero()
		return nil
	}
	path := newPathStack()
	return d.decode(*src, target.Elem(), true, &path)
}

// planFor returns the cached plan for a struct type, building it on first use.
//
// Two recent entries in front of the sync.Map keep the common case of decoding
// a type that contains another type (a tenant with nested objects, for example)
// free of map lookups. Plans are immutable once published, so the atomic
// pointers need no further synchronisation; a concurrent miss only repeats the
// map lookup.
func (d *Decoder) planFor(typ reflect.Type) *structPlan {
	if plan := d.recentA.Load(); plan != nil && plan.typ == typ {
		return plan
	}
	if plan := d.recentB.Load(); plan != nil && plan.typ == typ {
		return plan
	}

	plan, ok := d.plans.Load(typ)
	if ok {
		cached := plan.(*structPlan)
		d.cachePlan(cached)
		return cached
	}

	built := buildStructPlan(typ, d.tags)
	actual, _ := d.plans.LoadOrStore(typ, built)
	cached := actual.(*structPlan)
	d.cachePlan(cached)
	return cached
}

// cachePlan publishes plan in one of the two recent slots, alternating which
// slot is replaced.
func (d *Decoder) cachePlan(plan *structPlan) {
	if d.recentSeq.Add(1)&1 == 0 {
		d.recentA.Store(plan)
		return
	}
	d.recentB.Store(plan)
}

// decode decodes v into dst. path is the configuration path of dst. hooks
// reports whether decode hooks are consulted for this value; the value produced
// by a hook is decoded with hooks disabled so a hook cannot loop on its own
// output.
func (d *Decoder) decode(v value.Value, dst reflect.Value, hooks bool, path *pathStack) error {
	if v.IsNull() {
		dst.SetZero()
		return nil
	}
	typ := dst.Type()

	// Fast path for exact predeclared scalar destinations: these need neither
	// named-type/TextUnmarshaler inspection nor the generic assignment
	// dispatch. This is the common case for configuration structs.
	switch v.Kind() {
	case value.KindString:
		if typ == stringType {
			dst.SetString(v.String())
			return nil
		}
	case value.KindInt:
		switch typ {
		case intType, int64Type:
			dst.SetInt(v.Int())
			return nil
		}
	case value.KindBool:
		if typ == boolType {
			dst.SetBool(v.Bool())
			return nil
		}
	case value.KindFloat:
		if typ == float64Type {
			dst.SetFloat(v.Float())
			return nil
		}
	}

	// A named string type can validate its input through encoding.TextUnmarshaler,
	// so it must see the raw text before plain assignment. The predeclared
	// string type and the well-known named types skip this check, which keeps
	// ordinary string fields free of type inspection. Other named destination
	// types are checked after the assignment attempt below, where their kind
	// already rules out a plain assignment.
	if v.Kind() == value.KindString {
		switch {
		case typ == stringType, typ == timeType, typ == durationType, typ == urlType:
		case typ.Kind() == reflect.String:
			if handled, err := d.unmarshalText(v.String(), dst, path); handled {
				return err
			}
		}
	}

	if done, err := d.assign(v, dst, path); done {
		return err
	}

	if v.Kind() == value.KindString && typ.Kind() != reflect.String && !isWellKnownType(typ) {
		if handled, err := d.unmarshalText(v.String(), dst, path); handled {
			return err
		}
	}

	if hooks && d.hasHooks && !isEmptyInterface(typ) {
		converted, used, err := d.applyHook(v, typ, path)
		if err != nil {
			return err
		}
		if used {
			if !converted.IsValid() {
				dst.SetZero()
				return nil
			}
			if converted.Type() == typ {
				dst.Set(converted)
				return nil
			}
			return d.decode(value.From(converted.Interface()), dst, false, path)
		}
	}

	// Only string and numeric values can carry a time, duration or URL.
	if kind := v.Kind(); kind == value.KindString || kind == value.KindInt ||
		(kind == value.KindFloat && d.weak) {
		if handled, err := d.decodeSpecial(v, dst, path); handled {
			return err
		}
	}

	switch typ.Kind() {
	case reflect.Pointer:
		return d.decodePointer(v, dst, path)
	case reflect.Interface:
		return d.decodeInterface(v, dst, path)
	}

	if v.Kind() == value.KindAny {
		return d.decodeAny(v, dst, path)
	}

	switch typ.Kind() {
	case reflect.Slice:
		return d.decodeSlice(v, dst, path)
	case reflect.Array:
		return d.decodeArray(v, dst, path)
	case reflect.Map:
		return d.decodeMap(v, dst, path)
	case reflect.Struct:
		return d.decodeStruct(v, dst, path)
	}

	if d.weak {
		if handled, err := d.weakValue(v, dst, path); handled {
			return err
		}
	}
	return d.typeError(path, v, typ)
}

// applyHook runs the configured hooks. It reports whether a hook handled the
// conversion and returns the converted value.
func (d *Decoder) applyHook(v value.Value, to reflect.Type, path *pathStack) (reflect.Value, bool, error) {
	from := hookSourceType(v)
	converted, err := d.hooks(from, to, v.Interface())
	if err != nil {
		return reflect.Value{}, true, path.wrap(err)
	}
	if converted == nil {
		return reflect.Value{}, true, nil
	}
	convertedType := reflect.TypeOf(converted)
	if convertedType == from {
		// The hook declined the conversion.
		return reflect.Value{}, false, nil
	}
	return reflect.ValueOf(converted), true, nil
}

// decodePointer allocates a destination pointer and decodes into its element.
func (d *Decoder) decodePointer(v value.Value, dst reflect.Value, path *pathStack) error {
	if dst.IsNil() {
		dst.Set(reflect.New(dst.Type().Elem()))
	}
	return d.decode(v, dst.Elem(), true, path)
}

// decodeInterface decodes into an interface destination.
func (d *Decoder) decodeInterface(v value.Value, dst reflect.Value, path *pathStack) error {
	typ := dst.Type()
	raw := v.Interface()
	if typ.NumMethod() == 0 {
		dst.Set(reflect.ValueOf(raw))
		return nil
	}
	if raw != nil {
		converted := reflect.ValueOf(raw)
		if converted.Type().Implements(typ) {
			dst.Set(converted)
			return nil
		}
	}
	return d.typeError(path, v, typ)
}

// decodeAny decodes a value that wraps an arbitrary Go value, as produced by
// document conversion or by a decode hook.
func (d *Decoder) decodeAny(v value.Value, dst reflect.Value, path *pathStack) error {
	raw := v.AnyValue()
	if raw == nil {
		dst.SetZero()
		return nil
	}
	typ := dst.Type()
	converted := reflect.ValueOf(raw)

	if converted.Type().AssignableTo(typ) {
		dst.Set(converted)
		return nil
	}
	if typ.Kind() == reflect.Interface {
		if converted.Type().Implements(typ) {
			dst.Set(converted)
			return nil
		}
		return d.typeError(path, v, typ)
	}
	if converted.Kind() == reflect.Pointer && !converted.IsNil() &&
		converted.Elem().Type().AssignableTo(typ) {
		dst.Set(converted.Elem())
		return nil
	}

	switch converted.Kind() {
	case reflect.Slice, reflect.Array:
		if typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			return d.decodeReflectSlice(converted, dst, path)
		}
	case reflect.Map:
		if typ.Kind() == reflect.Map {
			return d.decodeReflectMap(converted, dst, path)
		}
	}

	if isNumericKind(converted.Kind()) && isNumericKind(typ.Kind()) {
		if done, err := d.assign(fromReflect(converted), dst, path); done {
			return err
		}
	}
	if converted.Kind() == reflect.String && typ.Kind() == reflect.Slice &&
		typ.Elem().Kind() == reflect.Uint8 {
		d.setBytes(dst, []byte(converted.String()))
		return nil
	}
	if converted.Kind() == reflect.Slice && converted.Type().Elem().Kind() == reflect.Uint8 &&
		typ.Kind() == reflect.String {
		dst.SetString(string(converted.Bytes()))
		return nil
	}

	inner := fromReflect(converted)
	if inner.Kind() == value.KindAny {
		return d.typeError(path, v, typ)
	}
	return d.decode(inner, dst, false, path)
}

// decodeReflectSlice copies a reflected slice or array into a slice or array
// destination.
func (d *Decoder) decodeReflectSlice(src reflect.Value, dst reflect.Value, path *pathStack) error {
	typ := dst.Type()
	length := src.Len()
	if typ.Kind() == reflect.Array {
		if length != typ.Len() {
			return path.errorf("cannot decode %s with %d elements into %s", src.Type(), length, typ)
		}
		for i := range length {
			path.pushIndex(i)
			err := d.decode(fromReflect(src.Index(i)), dst.Index(i), true, path)
			path.pop()
			if err != nil {
				return err
			}
		}
		return nil
	}

	out := reflect.MakeSlice(typ, length, length)
	for i := range length {
		path.pushIndex(i)
		err := d.decode(fromReflect(src.Index(i)), out.Index(i), true, path)
		path.pop()
		if err != nil {
			return err
		}
	}
	dst.Set(out)
	return nil
}

// decodeReflectMap copies a reflected map into a map destination.
func (d *Decoder) decodeReflectMap(src reflect.Value, dst reflect.Value, path *pathStack) error {
	typ := dst.Type()
	keyType := typ.Key()
	out := reflect.MakeMapWithSize(typ, src.Len())
	temp := reflect.New(typ.Elem()).Elem()

	iter := src.MapRange()
	for iter.Next() {
		key := iter.Key()
		var mapKey reflect.Value
		switch {
		case key.Type().AssignableTo(keyType):
			mapKey = key
		case key.Type().ConvertibleTo(keyType):
			mapKey = key.Convert(keyType)
		default:
			return path.errorf("cannot decode map key of type %s into %s", key.Type(), keyType)
		}

		temp.SetZero()
		path.pushMapKey(fmt.Sprint(key.Interface()))
		err := d.decode(fromReflect(iter.Value()), temp, true, path)
		path.pop()
		if err != nil {
			return err
		}
		out.SetMapIndex(mapKey, temp)
	}

	dst.Set(out)
	return nil
}

// decodeSlice decodes a list into a slice destination.
func (d *Decoder) decodeSlice(v value.Value, dst reflect.Value, path *pathStack) error {
	typ := dst.Type()
	if v.Kind() == value.KindString && typ.Elem().Kind() == reflect.Uint8 {
		d.setBytes(dst, []byte(v.String()))
		return nil
	}
	if v.Kind() != value.KindList {
		if d.weak && v.IsScalar() {
			return d.weakList(v, dst, path)
		}
		return d.typeError(path, v, typ)
	}

	items := v.List()
	out := reflect.MakeSlice(typ, len(items), len(items))
	for i := range items {
		path.pushIndex(i)
		err := d.decode(items[i], out.Index(i), true, path)
		path.pop()
		if err != nil {
			return err
		}
	}
	dst.Set(out)
	return nil
}

// decodeArray decodes a list into an array destination of the same length.
func (d *Decoder) decodeArray(v value.Value, dst reflect.Value, path *pathStack) error {
	typ := dst.Type()
	if v.Kind() == value.KindString && typ.Elem().Kind() == reflect.Uint8 {
		text := v.String()
		if typ.Len() == len(text) {
			for i := 0; i < typ.Len(); i++ {
				dst.Index(i).SetUint(uint64(text[i]))
			}
			return nil
		}
	}
	if v.Kind() != value.KindList {
		if d.weak && v.IsScalar() {
			return d.weakList(v, dst, path)
		}
		return d.typeError(path, v, typ)
	}

	items := v.List()
	if len(items) != typ.Len() {
		return path.errorf("cannot decode list of %d elements into %s", len(items), typ)
	}
	for i := range items {
		path.pushIndex(i)
		err := d.decode(items[i], dst.Index(i), true, path)
		path.pop()
		if err != nil {
			return err
		}
	}
	return nil
}

// decodeMap decodes an object into a map destination with string keys.
func (d *Decoder) decodeMap(v value.Value, dst reflect.Value, path *pathStack) error {
	typ := dst.Type()
	if v.Kind() != value.KindObject {
		return d.typeError(path, v, typ)
	}
	keyType := typ.Key()
	if keyType.Kind() != reflect.String {
		return path.errorf("cannot decode object into %s: map keys must be strings", typ)
	}

	object := v.Object()
	out := reflect.MakeMapWithSize(typ, object.Len())
	// One addressable temporary per key and value is reused for every entry:
	// SetMapIndex copies both key and value into the map, so the destination
	// does not keep a reference to the temporaries.
	var temp, keyValue reflect.Value
	if object.Len() > 0 {
		temp = reflect.New(typ.Elem()).Elem()
		keyValue = reflect.New(keyType).Elem()
	}
	for i := 0; i < object.Len(); i++ {
		key, item := object.At(i)
		keyValue.SetString(key)
		temp.SetZero()
		path.pushMapKey(key)
		err := d.decode(item, temp, true, path)
		path.pop()
		if err != nil {
			return err
		}
		out.SetMapIndex(keyValue, temp)
	}
	dst.Set(out)
	return nil
}

// decodeStruct decodes an object into a struct destination using the cached
// plan for its type.
func (d *Decoder) decodeStruct(v value.Value, dst reflect.Value, path *pathStack) error {
	typ := dst.Type()
	if v.Kind() != value.KindObject {
		return d.typeError(path, v, typ)
	}

	plan := d.planFor(typ)
	if plan.err != nil {
		return path.errorf("%v", plan.err)
	}

	object := v.Object()
	var remainBuf [8]int32
	var remain []int32
	if plan.remain >= 0 {
		remain = remainBuf[:0]
	}

	for i := 0; i < object.Len(); i++ {
		key, item := object.At(i)
		fieldIndex, ok := plan.lookup(key)
		if !ok {
			if remain != nil {
				remain = append(remain, int32(i))
				continue
			}
			if d.strict {
				return path.errorAtField(key, fmt.Sprintf("unknown configuration key for %s", typ))
			}
			continue
		}

		field := &plan.fields[fieldIndex]
		target, err := fieldValue(dst, field.index)
		if err != nil {
			return path.errorAtField(key, err.Error())
		}
		path.pushField(key)
		err = d.decode(item, target, true, path)
		path.pop()
		if err != nil {
			return err
		}
	}

	if len(remain) > 0 {
		field := &plan.fields[plan.remain]
		target, err := fieldValue(dst, field.index)
		if err != nil {
			return path.wrap(err)
		}
		return d.decodeRemain(object, remain, target, path)
	}
	return nil
}

// decodeRemain fills the remain field of a struct with the object keys that no
// field claimed.
func (d *Decoder) decodeRemain(object *value.Object, indexes []int32, dst reflect.Value, path *pathStack) error {
	typ := dst.Type()
	out := reflect.MakeMapWithSize(typ, len(indexes))
	temp := reflect.New(typ.Elem()).Elem()
	keyValue := reflect.New(typ.Key()).Elem()

	for _, index := range indexes {
		key, item := object.At(int(index))
		keyValue.SetString(key)
		temp.SetZero()
		path.pushMapKey(key)
		err := d.decode(item, temp, true, path)
		path.pop()
		if err != nil {
			return err
		}
		out.SetMapIndex(keyValue, temp)
	}

	dst.Set(out)
	return nil
}

// fieldValue walks the field index path of a struct value, allocating nil
// embedded pointers on the way.
func fieldValue(dst reflect.Value, index []int) (reflect.Value, error) {
	if len(index) == 1 {
		field := dst.Field(index[0])
		if !field.CanSet() {
			return reflect.Value{}, fmt.Errorf("field %s of %s cannot be set",
				dst.Type().Field(index[0]).Name, dst.Type())
		}
		return field, nil
	}
	current := dst
	for depth, field := range index {
		current = current.Field(field)
		if depth == len(index)-1 {
			break
		}
		if current.Kind() == reflect.Pointer {
			if current.IsNil() {
				if !current.CanSet() {
					return reflect.Value{}, fmt.Errorf("cannot allocate embedded pointer %s", current.Type())
				}
				current.Set(reflect.New(current.Type().Elem()))
			}
			current = current.Elem()
		}
	}
	if !current.CanSet() {
		return reflect.Value{}, fmt.Errorf("field %s of %s cannot be set",
			dst.Type().FieldByIndex(index).Name, dst.Type())
	}
	return current, nil
}

// setBytes assigns raw bytes to a byte slice destination, including named byte
// slice types.
func (d *Decoder) setBytes(dst reflect.Value, data []byte) {
	typ := dst.Type()
	if typ == byteSliceType {
		dst.SetBytes(data)
		return
	}
	out := reflect.MakeSlice(typ, len(data), len(data))
	for i := range data {
		out.Index(i).SetUint(uint64(data[i]))
	}
	dst.Set(out)
}

var byteSliceType = reflect.TypeFor[[]byte]()

// fromReflect converts a reflected value into a configuration value without
// deep-copying containers.
func fromReflect(src reflect.Value) value.Value {
	if !src.IsValid() {
		return value.Null()
	}
	switch src.Kind() {
	case reflect.Interface, reflect.Pointer:
		if src.IsNil() {
			return value.Null()
		}
		return fromReflect(src.Elem())
	case reflect.Bool:
		return value.Bool(src.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int(src.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		unsigned := src.Uint()
		if unsigned > math.MaxInt64 {
			return value.Float(float64(unsigned))
		}
		return value.Int(int64(unsigned))
	case reflect.Float32, reflect.Float64:
		return value.Float(src.Float())
	case reflect.String:
		return value.String(src.String())
	case reflect.Slice, reflect.Array:
		length := src.Len()
		items := make([]value.Value, length)
		for i := range length {
			items[i] = fromReflect(src.Index(i))
		}
		return value.List(items)
	default:
		return value.Any(src.Interface())
	}
}

// hookSourceType returns the Go type reported to hooks for a value.
func hookSourceType(v value.Value) reflect.Type {
	switch v.Kind() {
	case value.KindBool:
		return boolType
	case value.KindInt:
		return int64Type
	case value.KindFloat:
		return float64Type
	case value.KindString:
		return stringType
	case value.KindList:
		return anySliceType
	case value.KindObject:
		return stringAnyMapType
	case value.KindAny:
		return reflect.TypeOf(v.AnyValue())
	}
	return nil
}

// isEmptyInterface reports whether typ is the empty interface.
func isEmptyInterface(typ reflect.Type) bool {
	return typ.Kind() == reflect.Interface && typ.NumMethod() == 0
}
