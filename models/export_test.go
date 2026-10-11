package models

import (
	"reflect"
	"unsafe"
)

func UnsafePointer[V any](pointer *V) unsafe.Pointer { return unsafe.Pointer(pointer) }

func typeOf[V any]() reflect.Type { return reflect.TypeFor[V]() }
