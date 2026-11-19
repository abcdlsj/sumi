package protocol

import "testing"

func TestCompatibleVersionRange(t *testing.T) {
	cases := []struct {
		v    int
		want bool
	}{{MinVersion - 1, false}, {MinVersion, true}, {Version, true}, {Version + 1, false}}
	for _, c := range cases {
		if Compatible(c.v) != c.want {
			t.Fatalf("Compatible(%d) = %v", c.v, !c.want)
		}
	}
	if !Has(Supported, CapAgentActivity) || Has(nil, CapAgentActivity) {
		t.Fatalf("capability lookup")
	}
}
