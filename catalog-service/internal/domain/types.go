package domain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/an-vt/ecom/platform-go/errs"
)

// Type of product. Each type has a strategy that knows its required attributes (the monolith's
// ProductFactory.registerProductType).
type Type string

const (
	TypeElectronic Type = "Electronic"
	TypeClothing   Type = "Clothing"
	TypeFurniture  Type = "Furniture"
)

// TypeStrategy defines what a product of one type must carry.
type TypeStrategy interface {
	Type() Type
	// RequiredAttributes are attribute keys that must be present and non-empty.
	RequiredAttributes() []string
}

type staticStrategy struct {
	t        Type
	required []string
}

func (s staticStrategy) Type() Type                   { return s.t }
func (s staticStrategy) RequiredAttributes() []string { return s.required }

// TypeRegistry maps a product type to its strategy.
type TypeRegistry struct{ m map[Type]TypeStrategy }

// NewTypeRegistry creates a registry from strategies.
func NewTypeRegistry(strategies ...TypeStrategy) *TypeRegistry {
	r := &TypeRegistry{m: map[Type]TypeStrategy{}}
	for _, s := range strategies {
		r.m[s.Type()] = s
	}
	return r
}

// DefaultTypes is the catalogue of the monolith: Electronic, Clothing and Furniture.
func DefaultTypes() *TypeRegistry {
	return NewTypeRegistry(
		staticStrategy{TypeElectronic, []string{"manufacturer", "model", "color"}},
		staticStrategy{TypeClothing, []string{"brand", "size", "material"}},
		staticStrategy{TypeFurniture, []string{"brand", "size", "material"}},
	)
}

// Types lists the registered types.
func (r *TypeRegistry) Types() []Type {
	out := make([]Type, 0, len(r.m))
	for t := range r.m {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Validate checks that t is known and attrs has every required attribute.
func (r *TypeRegistry) Validate(t Type, attrs map[string]any) error {
	s, ok := r.m[t]
	if !ok {
		return errs.InvalidArgument(CodeInvalidType, fmt.Sprintf("unknown product type %q", string(t)))
	}
	var missing []string
	for _, key := range s.RequiredAttributes() {
		v, present := attrs[key]
		if !present || v == nil || strings.TrimSpace(fmt.Sprint(v)) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return errs.InvalidArgument(CodeInvalidAttribute, fmt.Sprintf("%s products need the attributes: %s", t, strings.Join(missing, ", ")))
	}
	return nil
}
