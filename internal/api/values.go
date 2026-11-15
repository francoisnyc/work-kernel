package api

import (
	"math"
	"strconv"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Field values are plain JSON on the wire (google.protobuf.Value), converted
// against the field's type in the Item's Type. Only the shape of an int
// differs from what the kernel stores: int64 does not fit a JSON number, so
// an int goes out as a string and comes in as a number or a numeric string.
// Everything else passes through, and the kernel's own check (CheckFields)
// refuses what does not fit, naming the field.

// fieldsIn converts a request's fields for the kernel. A nil Struct is no
// fields.
func fieldsIn(ty *typedef.Type, s *structpb.Struct) (map[string]any, *connect.Error) {
	if s == nil || len(s.Fields) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(s.Fields))
	for id, v := range s.Fields {
		f, declared := ty.Field(id)
		if declared && f.Type == typedef.Int {
			switch x := v.Kind.(type) {
			case *structpb.Value_StringValue:
				n, err := strconv.ParseInt(x.StringValue, 10, 64)
				if err != nil {
					return nil, badField("fields."+id, "want a whole number")
				}
				out[id] = n
				continue
			case *structpb.Value_NumberValue:
				// A whole number in range becomes an int64; anything else
				// stays a float64, which the kernel refuses for an int.
				if x.NumberValue == math.Trunc(x.NumberValue) && math.Abs(x.NumberValue) <= 1<<53-1 {
					out[id] = int64(x.NumberValue)
					continue
				}
			}
		}
		// AsInterface gives the plain Go value: string, float64, bool, nil,
		// []any or map[string]any.
		out[id] = v.AsInterface()
	}
	return out, nil
}

// fieldsOut converts an Item's stored fields for the wire.
func fieldsOut(ty *typedef.Type, fields map[string]any) (*structpb.Struct, error) {
	out := &structpb.Struct{Fields: make(map[string]*structpb.Value, len(fields))}
	for id, v := range fields {
		if f, ok := ty.Field(id); ok && f.Type == typedef.Int {
			if n, ok := typedef.AsInt(v); ok {
				out.Fields[id] = structpb.NewStringValue(strconv.FormatInt(n, 10))
				continue
			}
		}
		pv, err := structpb.NewValue(v)
		if err != nil {
			return nil, err
		}
		out.Fields[id] = pv
	}
	return out, nil
}
