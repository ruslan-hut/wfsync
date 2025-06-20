package validate

import (
	"errors"
	"fmt"
	"github.com/go-playground/validator/v10"
	"reflect"
	"strings"
)

// Struct validates a single struct object
func Struct(s interface{}) error {
	if s == nil {
		return fmt.Errorf("is nil")
	}
	if !isStruct(s) {
		return fmt.Errorf("not a struct")
	}
	var validationErrors validator.ValidationErrors
	var invalidValidationError *validator.InvalidValidationError

	validate := validator.New()
	validate.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" {
			return ""
		}
		return name
	})
	err := validate.Struct(s)
	if err == nil {
		return nil
	}

	if errors.As(err, &validationErrors) {
		message := ""
		for _, fieldErr := range validationErrors {
			if len(message) > 0 {
				message += "; "
			}
			message += fmt.Sprintf("%s %s", fieldErr.Field(), fieldErr.Tag())
		}
		return fmt.Errorf(message)
	} else if errors.As(err, &invalidValidationError) {
		return fmt.Errorf("invalid validation error: %w", err)
	} else {
		return fmt.Errorf("unknown validation error: %w", err)
	}
}

func isStruct(s interface{}) bool {
	r := reflect.TypeOf(s)
	if r.Kind() == reflect.Ptr {
		r = r.Elem()
	}
	return r.Kind() == reflect.Struct
}
