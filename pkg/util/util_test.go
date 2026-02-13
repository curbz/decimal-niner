package util

import (
	"encoding/base64"
	"reflect"
	"testing"
)

func TestDecodeNullTerminatedString(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want []string
	}{
		{name: "no empty elements", raw: []byte("A\x00B\x00C\x00D"), want: []string{"A", "B", "C", "D"}},
		{name: "middle element empty", raw: []byte("A\x00\x00C\x00D"), want: []string{"A", "", "C", "D"}},
		{name: "trailing element empty", raw: []byte("A\x00B\x00\x00"), want: []string{"A", "B", ""}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			enc := base64.StdEncoding.EncodeToString(tc.raw)
			got, err := DecodeNullTerminatedString(enc)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s: decoded mismatch\nwant: %#v\ngot:  %#v", tc.name, tc.want, got)
			}
		})
	}
}
