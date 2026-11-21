package lambe

import (
	"reflect"

	"github.com/andrew-bierman/abacus/internal/core"
)

// sanitizeStrings walks a decoded value and sanitizes every string field in place, so no
// front-end ever receives control characters from the daemon or tracker.
func sanitizeStrings(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			sanitizeStrings(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				sanitizeStrings(v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			sanitizeStrings(v.Index(i))
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(core.Sanitize(v.String()))
		}
	}
}

func sanitizeView(v *View) { sanitizeStrings(reflect.ValueOf(v)) }
