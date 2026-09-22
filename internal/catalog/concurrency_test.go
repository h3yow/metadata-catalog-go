package catalog

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentDuplicateReports：N 个 goroutine 并发上报完全相同的实体，
// 期望恰好 1 次 created，其余全部 noop，最终版本 1、历史只有 1 条。
func TestConcurrentDuplicateReports(t *testing.T) {
	c := New()
	const n = 50
	var wg sync.WaitGroup
	created, noop := 0, 0
	var mu sync.Mutex
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			res, err := c.Register(baseSpec())
			assertNoErr(t, err)
			mu.Lock()
			switch res.Change {
			case ChangeCreated:
				created++
			case ChangeNoop:
				noop++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	judge(t, fmt.Sprintf("%d goroutines register identical orders", n),
		fmt.Sprintf("created=%d noop=%d", created, noop), nil)
	if created != 1 || noop != n-1 {
		t.Fatalf("want exactly 1 created and %d noop, got created=%d noop=%d", n-1, created, noop)
	}
	got, _ := c.Get("orders")
	if got.Version != 1 {
		t.Fatalf("version must stay 1, got %d", got.Version)
	}
	hist, _ := c.History("orders")
	if len(hist) != 1 {
		t.Fatalf("history must have 1 entry, got %d", len(hist))
	}
}

// TestConcurrentNoLostUpdates：不同 goroutine 并发上报同一实体，
// 各自携带一个互不相同的新标签（字段集合保持不变）。
// 标签按并集合入，任何一个丢失更新都会让最终标签数少于 n。
func TestConcurrentNoLostUpdates(t *testing.T) {
	c := New()
	_, err := c.Register(baseSpec())
	assertNoErr(t, err)

	const n = 30
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			spec := baseSpec()
			spec.Tags = []string{"finance", fmt.Sprintf("tag-%02d", i)}
			res, err := c.Register(spec)
			assertNoErr(t, err)
			if res.Change != ChangeCompatible {
				t.Errorf("goroutine %d: want compatible, got %s", i, res.Change)
			}
		}()
	}
	wg.Wait()

	got, _ := c.Get("orders")
	if want := 1 + n; len(got.Tags) != want {
		t.Fatalf("lost update: want %d tags, got %d (%v)", want, len(got.Tags), got.Tags)
	}
	if got.Version != 1+n {
		t.Fatalf("want final version %d, got %d", 1+n, got.Version)
	}
	hist, _ := c.History("orders")
	if len(hist) != 1+n {
		t.Fatalf("want %d history entries, got %d", 1+n, len(hist))
	}
	t.Logf("INPUT: %d goroutines add distinct tags => JUDGEMENT: version=%d tags=%d history=%d",
		n, got.Version, len(got.Tags), len(hist))
}

// TestConcurrentReadConsistency：一边做累积式兼容演进与重复血缘登记，一边持续读，
// 读到的任何快照都必须字段数与版本自洽，检索结果不得出现中间态。
func TestConcurrentReadConsistency(t *testing.T) {
	c := New()
	_, _ = c.Register(baseSpec())
	_, _ = c.Register(EntitySpec{Name: "audit", Source: "s", Kind: "table",
		Fields: []FieldSpec{{Name: "id", Type: "int64"}}})

	stop := make(chan struct{})
	writerDone := make(chan struct{})

	// 写者：每次上报都包含此前全部字段再加一个新可选字段（累积演进），
	// 并反复登记同一条血缘，重复登记必须始终被识别为 duplicate。
	go func() {
		defer close(writerDone)
		fields := append([]FieldSpec(nil), baseSpec().Fields...)
		for i := 0; i < 100; i++ {
			spec := baseSpec()
			spec.Fields = append(append([]FieldSpec(nil), fields...),
				FieldSpec{Name: fmt.Sprintf("f_%02d", i), Type: "string", Optional: true})
			if _, err := c.Register(spec); err != nil {
				t.Errorf("writer register: %v", err)
				return
			}
			fields = append(fields, FieldSpec{Name: fmt.Sprintf("f_%02d", i), Type: "string", Optional: true})
			r, err := c.AddLineage("orders", "audit", "sync", nil)
			if err != nil {
				t.Errorf("writer lineage: %v", err)
				return
			}
			if i > 0 && !r.Duplicate {
				t.Errorf("iteration %d: repeated lineage must be duplicate", i)
				return
			}
		}
	}()

	// 读者：v1 有 2 个字段，之后每升一版恰好多 1 个字段；自洽即无中间态。
	readerWG := sync.WaitGroup{}
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if got, err := c.Get("orders"); err == nil {
				if want := 2 + (got.Version - 1); len(got.Fields) != want {
					t.Errorf("inconsistent read: version=%d fields=%d want=%d",
						got.Version, len(got.Fields), want)
					return
				}
			}
			_ = c.Search(Query{Keyword: "order"})
			d, err := c.Downstream("orders")
			if err == nil && len(d) != 0 && len(d) != 1 {
				t.Errorf("downstream must be empty or {audit}, got %v", d)
				return
			}
		}
	}()

	<-writerDone
	close(stop)
	readerWG.Wait()
	judge(t, "100 cumulative compatible evolutions + repeated lineage + concurrent reads",
		"no inconsistent snapshot observed; final state verified", nil)

	got, _ := c.Get("orders")
	if got.Version != 101 || len(got.Fields) != 102 {
		t.Fatalf("final state want v101/102 fields, got v%d/%d fields", got.Version, len(got.Fields))
	}
}
