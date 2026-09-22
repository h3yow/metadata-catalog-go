package catalog

import (
	"reflect"
	"testing"
)

func regEntities(c *Catalog, names ...string) {
	t := &testing.T{}
	for _, n := range names {
		spec := EntitySpec{Name: n, Source: "seed", Kind: "table",
			Fields: []FieldSpec{{Name: "id", Type: "int64"}}}
		if _, err := c.Register(spec); err != nil {
			t.Fatalf("seed %s: %v", n, err)
		}
	}
}

func TestLineageBasicAndDuplicate(t *testing.T) {
	c := New()
	regEntities(c, "a", "b")

	r1, err := c.AddLineage("a", "b", "job-x", map[string]string{"sql": "insert"})
	judge(t, "lineage a->b from job-x", r1, err)
	assertNoErr(t, err)
	if !r1.Accepted || r1.Duplicate {
		t.Fatalf("first edge accepted, got %+v", r1)
	}

	// 同一关系重复登记：必须被识别而不是静默覆盖。
	r2, err := c.AddLineage("a", "b", "job-y", map[string]string{"sql": "overwrite?"})
	judge(t, "repeat lineage a->b from job-y with different props", r2, err)
	assertNoErr(t, err)
	if r2.Accepted || !r2.Duplicate {
		t.Fatalf("duplicate edge must be flagged, got %+v", r2)
	}

	down, err := c.Downstream("a")
	assertNoErr(t, err)
	if !reflect.DeepEqual(down, []string{"b"}) {
		t.Fatalf("a downstream want [b], got %v", down)
	}
	up, err := c.Upstream("b")
	assertNoErr(t, err)
	if !reflect.DeepEqual(up, []string{"a"}) {
		t.Fatalf("b upstream want [a], got %v", up)
	}
}

func TestLineageSelfLoopAndMissing(t *testing.T) {
	c := New()
	regEntities(c, "a")
	_, err := c.AddLineage("a", "a", "job", nil)
	judge(t, "lineage a->a (self loop)", nil, err)
	assertReason(t, err, ReasonSelfLoop)

	_, err = c.AddLineage("a", "ghost", "job", nil)
	judge(t, "lineage to unregistered downstream", nil, err)
	assertReason(t, err, ReasonEntityNotFound)

	if _, err := c.Downstream("ghost"); err == nil {
		t.Fatalf("downstream of unknown entity must error")
	} else {
		assertReason(t, err, ReasonEntityNotFound)
	}
}

// 构造一个环：a -> b -> c -> a，并附加多路径 a -> c。
// 遍历必须终止、去重、顺序稳定可复现。
func TestLineageCycleAndMultipath(t *testing.T) {
	c := New()
	regEntities(c, "a", "b", "c")
	edges := [][2]string{{"a", "b"}, {"b", "c"}, {"c", "a"}, {"a", "c"}}
	for _, e := range edges {
		r, err := c.AddLineage(e[0], e[1], "seeder", nil)
		assertNoErr(t, err)
		if !r.Accepted {
			t.Fatalf("edge %v should be accepted", e)
		}
	}

	want := []string{"b", "c"}
	for i := 0; i < 5; i++ { // 多次调用结果必须稳定可复现
		d, err := c.Downstream("a")
		assertNoErr(t, err)
		if !reflect.DeepEqual(d, want) {
			t.Fatalf("a downstream iter %d want %v, got %v", i, want, d)
		}
	}

	// b 的传递下游包含 c，但沿 c->a->b 回到自己时必须被 visited 截断。
	d, _ := c.Downstream("b")
	if !reflect.DeepEqual(d, []string{"a", "c"}) {
		t.Fatalf("b downstream want [a c] (cycle safe), got %v", d)
	}

	// c 的上游通过两条路径到达 a（直接 c->a 反向，以及 c->a->b->... ），只计一次。
	up, _ := c.Upstream("c")
	if !reflect.DeepEqual(up, []string{"a", "b"}) {
		t.Fatalf("c upstream want [a b], got %v", up)
	}
}

// 菱形：a -> b -> d, a -> c -> d，d 必须只出现一次且顺序稳定。
func TestLineageDiamond(t *testing.T) {
	c := New()
	regEntities(c, "a", "b", "c", "d")
	for _, e := range [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}, {"c", "d"}} {
		_, err := c.AddLineage(e[0], e[1], "seeder", nil)
		assertNoErr(t, err)
	}
	d, _ := c.Downstream("a")
	if !reflect.DeepEqual(d, []string{"b", "c", "d"}) {
		t.Fatalf("diamond downstream want [b c d], got %v", d)
	}
}
