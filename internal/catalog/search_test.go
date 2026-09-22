package catalog

import (
	"reflect"
	"testing"
)

func TestSearch(t *testing.T) {
	c := New()
	e1 := EntitySpec{
		Name: "orders", Source: "s1", Kind: "table",
		Fields: []FieldSpec{{Name: "user_id", Type: "int64", Semantic: "user.id"}},
		Tags:   []string{"finance", "pii"}, Owners: []string{"alice"},
		Properties: map[string]string{"zone": "availability-zone-a"},
	}
	e2 := EntitySpec{
		Name: "payments", Source: "s2", Kind: "table",
		Fields: []FieldSpec{{Name: "order_id", Type: "int64", Semantic: "order.id"}},
		Tags:   []string{"finance"}, Owners: []string{"bob"},
	}
	e3 := EntitySpec{
		Name: "clicks", Source: "s3", Kind: "topic",
		Tags: []string{"tracking"}, Owners: []string{"carol"},
	}
	for _, s := range []EntitySpec{e1, e2, e3} {
		_, err := c.Register(s)
		assertNoErr(t, err)
	}

	// 关键字匹配字段语义
	res := c.Search(Query{Keyword: "ORDER"}) // 大小写不敏感
	judge(t, `keyword ORDER case-insensitive`, names(res), nil)
	if !reflect.DeepEqual(names(res), []string{"orders", "payments"}) {
		t.Fatalf("keyword want orders,payments got %v", names(res))
	}

	// 标签 AND
	res = c.Search(Query{Tags: []string{"finance", "pii"}})
	if !reflect.DeepEqual(names(res), []string{"orders"}) {
		t.Fatalf("tags finance+pii want orders got %v", names(res))
	}

	// 负责人 ANY
	res = c.Search(Query{Owners: []string{"alice", "carol"}})
	if !reflect.DeepEqual(names(res), []string{"clicks", "orders"}) {
		t.Fatalf("owners any want clicks,orders got %v", names(res))
	}

	// kind 过滤
	res = c.Search(Query{Kind: "topic"})
	if !reflect.DeepEqual(names(res), []string{"clicks"}) {
		t.Fatalf("kind topic want clicks got %v", names(res))
	}

	// 组合条件
	res = c.Search(Query{Keyword: "zone", Tags: []string{"finance"}})
	if !reflect.DeepEqual(names(res), []string{"orders"}) {
		t.Fatalf("keyword+tag want orders got %v", names(res))
	}
}

// TestSearchSeesLatestState 验证检索与最新状态一致。
func TestSearchSeesLatestState(t *testing.T) {
	c := New()
	base := EntitySpec{
		Name: "x", Source: "s", Kind: "table",
		Fields: []FieldSpec{{Name: "id", Type: "int64"}},
		Tags:   []string{"old"},
	}
	_, _ = c.Register(base)
	evolved := base
	evolved.Tags = []string{"old", "fresh"}
	_, err := c.Register(evolved)
	assertNoErr(t, err)
	res := c.Search(Query{Tags: []string{"fresh"}})
	judge(t, "search tag fresh after evolution", names(res), nil)
	if len(res) != 1 || res[0].Version != 2 {
		t.Fatalf("search must reflect v2, got %+v", res)
	}
}

func names(es []Entity) []string {
	out := make([]string, len(es))
	for i := range es {
		out[i] = es[i].Name
	}
	return out
}
