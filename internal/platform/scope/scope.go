// Package scope is the set of branches (HolliHop offices) an admin may see
// (spec 13, D18). Every admin query takes one and filters in SQL with
//
//	(@all_offices::bool OR g.office_id = ANY (@office_ids::int[]))
//
// so a branch admin never receives another branch's rows, whatever the page
// asks for. Rows without an office are visible only to super admins.
package scope

// Offices: All for super admins; otherwise only IDs.
type Offices struct {
	All bool
	IDs []int32
}

// Has reports whether an office is in scope.
func (o Offices) Has(officeID int32) bool {
	if o.All {
		return true
	}
	for _, id := range o.IDs {
		if id == officeID {
			return true
		}
	}
	return false
}

// List is IDs, never nil, for the office_ids query parameter.
func (o Offices) List() []int32 {
	if o.IDs == nil {
		return []int32{}
	}
	return o.IDs
}
