package sim

import (
	"context"
	"testing"
)

func TestSimulationPassesEveryCheck(t *testing.T) {
	for _, opt := range []Options{
		{Buyers: 400, WindowMs: 0},
		{Buyers: 400, WindowMs: 200},
	} {
		rep, err := Run(context.Background(), opt)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range rep.Checks {
			if !c.OK {
				t.Errorf("%+v: %s (%s)", opt, c.Name, c.Detail)
			}
		}
		if !rep.Passed || len(rep.Winners) != stock || len(rep.Line) != lineSize {
			t.Fatalf("%+v: passed=%v winners=%d line=%d", opt, rep.Passed, len(rep.Winners), len(rep.Line))
		}
	}
}

func TestTooFewBuyersToSellOut(t *testing.T) {
	rep, err := Run(context.Background(), Options{Buyers: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passed || rep.Final.Held != 5 || rep.Final.Available != 15 {
		t.Fatalf("passed=%v final=%+v checks=%+v", rep.Passed, rep.Final, rep.Checks)
	}
}
