package jsonutil

import (
	"encoding/json"
	"testing"
)

func TestPythonNumberEncoding(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"1.0", "1.0"}, {"-0.0", "-0.0"}, {"1e15", "1000000000000000.0"}, {"1e16", "1e+16"}, {"1e-5", "1e-05"}, {"1e-4", "0.0001"}, {"9007199254740993", "9007199254740993"}, {"1e999", "Infinity"}, {"-1e999", "-Infinity"}} {
		t.Run(tc.input, func(t *testing.T) {
			if got := PythonJSON(json.Number(tc.input)); got != tc.want {
				t.Fatalf("%s != %s", got, tc.want)
			}
		})
	}
}
