package providerutil

import (
	"reflect"

	"github.com/xoai/sage-wiki/pkg/provider"
)

// IsNil reports whether p is nil, including an interface holding a typed-nil
// pointer. Provider constructors use it before storing interface values.
func IsNil(p provider.Provider) bool {
	if p == nil {
		return true
	}
	v := reflect.ValueOf(p)
	return (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil()
}
