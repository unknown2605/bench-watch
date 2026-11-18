package api

import (
	"testing"

	"benchwatch/server/internal/labels"
)

func TestModelLabelSchemaIsTheLabelsPattern(t *testing.T) {
	s := loadSpec(t).Components.Schemas["ModelLabel"].Value
	if s.Pattern != labels.ModelPattern {
		t.Errorf("spec pattern %q, labels.ModelPattern %q", s.Pattern, labels.ModelPattern)
	}
	if s.MaxLength == nil || *s.MaxLength != labels.ModelMaxLength {
		t.Errorf("spec maxLength %v, labels.ModelMaxLength %d", s.MaxLength, labels.ModelMaxLength)
	}
}
