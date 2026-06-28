package readbench

import (
	"reflect"
	"testing"
)

func TestParseSystemsCSVAcceptsFlatHAMT(t *testing.T) {
	got, err := ParseSystemsCSV("maltflat,flathamt")
	if err != nil {
		t.Fatalf("ParseSystemsCSV() error = %v", err)
	}
	want := []SystemName{SystemMALTFlat, SystemFlatHAMT}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("systems = %v, want %v", got, want)
	}
}
