package main

import "testing"

func TestCompactInt(t *testing.T) {
	for in, want := range map[int]string{
		0: "0", 999: "999", 9999: "9999", 10000: "10k", 581_000: "581k",
		1_200_000: "1.2M", 293_000_000: "293M", 1_357_365_806: "1.4B", 2_900_000_000: "2.9B",
	} {
		if got := compactInt(in); got != want {
			t.Errorf("compactInt(%d) = %q, want %q", in, got, want)
		}
	}
}
