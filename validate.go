package fastconf

// validator is implemented by configuration structs that validate themselves.
type validator interface {
	Validate() error
}

// validateStruct runs self-validation on the destination.
//
// Only a direct interface assertion is made. A pointer's method set includes
// the value-receiver methods of its element type, so this covers both
// Validate() on T and Validate() on *T. Reflecting on the element would copy
// the whole struct on every decode, which is measurable in benchmarks.
func validateStruct(destination any) error {
	if destination == nil {
		return nil
	}
	if selfValidator, ok := destination.(validator); ok {
		return selfValidator.Validate()
	}
	return nil
}
