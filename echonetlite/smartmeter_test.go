package echonetlite

import "testing"

func TestCumulativeEnergyUnitKWh(t *testing.T) {
	t.Parallel()

	testcases := []struct {
		name string
		code byte
		want float64
		err  bool
	}{
		{name: "1 kWh", code: 0x00, want: 1.0},
		{name: "0.1 kWh", code: 0x01, want: 0.1},
		{name: "0.01 kWh", code: 0x02, want: 0.01},
		{name: "0.001 kWh", code: 0x03, want: 0.001},
		{name: "0.0001 kWh", code: 0x04, want: 0.0001},
		{name: "10 kWh", code: 0x0A, want: 10.0},
		{name: "100 kWh", code: 0x0B, want: 100.0},
		{name: "1000 kWh", code: 0x0C, want: 1000.0},
		{name: "10000 kWh", code: 0x0D, want: 10000.0},
		{name: "unknown", code: 0x05, err: true},
	}

	for _, tc := range testcases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := CumulativeEnergyUnitKWh(tc.code)
			if tc.err {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestCumulativeEnergyKWh(t *testing.T) {
	t.Parallel()

	got := CumulativeEnergyKWh(12345, 2, 0.1)
	want := 2469.0
	if got != want {
		t.Fatalf("want %v, got %v", want, got)
	}
}
