package models

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseItemTypes(t *testing.T) {
	cases := []struct {
		raws    []string
		want    []ItemType
		wantErr bool
	}{
		{nil, nil, false},
		{[]string{""}, nil, false},
		{[]string{" , "}, nil, false},
		{[]string{"service"}, []ItemType{TypeService}, false},
		{[]string{"course,job", "mei_opportunity"}, []ItemType{TypeCourse, TypeJob, TypeMEIOpportunity}, false},
		{[]string{" job "}, []ItemType{TypeJob}, false},
		{[]string{"invalid_type"}, nil, true},
		{[]string{"SERVICE"}, nil, true},
		{[]string{"service,Job"}, nil, true},
		{[]string{"service", "x"}, nil, true},
	}

	for _, tc := range cases {
		got, err := ParseItemTypes(tc.raws)
		if tc.wantErr {
			if !errors.Is(err, ErrInvalidItemType) {
				t.Errorf("%q: err=%v, want ErrInvalidItemType", tc.raws, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: erro inesperado: %v", tc.raws, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: got %v, want %v", tc.raws, got, tc.want)
		}
	}
}

func TestItemTypes_CobreTodasAsConstantes(t *testing.T) {
	for _, typ := range []ItemType{TypeService, TypeCourse, TypeJob, TypeMEIOpportunity} {
		if !typ.Valid() {
			t.Errorf("%s deveria ser válido", typ)
		}
	}
	if got := ErrInvalidItemType.Error(); got != "types inválido: valores aceitos são service, course, job, mei_opportunity" {
		t.Errorf("mensagem = %q", got)
	}
}
