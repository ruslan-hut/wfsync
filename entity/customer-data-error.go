package entity

import (
	"errors"
	"fmt"
)

// CustomerDataError reports that wFirma rejected the buyer's data (email, NIP, address)
// with a field-level validation error. It is permanent: retrying cannot help until the
// order is corrected in OpenCart, so the retry queue fails such jobs immediately.
type CustomerDataError struct {
	Field   string
	Message string
}

func (e *CustomerDataError) Error() string {
	return fmt.Sprintf("wfirma rejected %s: %s", e.Field, e.Message)
}

// AsCustomerDataError returns the CustomerDataError wrapped in err, or nil.
func AsCustomerDataError(err error) *CustomerDataError {
	var e *CustomerDataError
	if errors.As(err, &e) {
		return e
	}
	return nil
}
