package approvals

import "testing"

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if r.Len() != 0 {
		t.Fatalf("new registry len = %d", r.Len())
	}
	if _, ok := r.Get("missing"); ok {
		t.Error("Get on empty registry found something")
	}
	if _, ok := r.Take(""); ok {
		t.Error("Take with empty id found something")
	}
	r.Remove("missing") // no-op

	answered := false
	r.Add("c1", Pending{Respond: func(ok bool) { answered = ok }, Tool: "bash", Input: `{"command":"ls"}`})
	r.Add("c2", Pending{}) // zero value is storable
	if r.Len() != 2 {
		t.Fatalf("len = %d, want 2", r.Len())
	}

	p, ok := r.Get("c1")
	if !ok || p.Tool != "bash" || p.Input != `{"command":"ls"}` {
		t.Fatalf("Get c1 = (%+v, %v)", p, ok)
	}
	if r.Len() != 2 {
		t.Error("Get removed the entry")
	}

	p, ok = r.Take("c1")
	if !ok || p.Respond == nil {
		t.Fatalf("Take c1 = (%+v, %v)", p, ok)
	}
	p.Respond(true)
	if !answered {
		t.Error("Respond did not reach the recorded closure")
	}
	if _, ok := r.Get("c1"); ok || r.Len() != 1 {
		t.Error("Take left the entry behind")
	}

	r.Add("c2", Pending{Tool: "Edit"}) // replace
	if p, _ := r.Get("c2"); p.Tool != "Edit" {
		t.Errorf("Add did not replace: %+v", p)
	}
	r.Remove("c2")
	if r.Len() != 0 {
		t.Error("Remove left the entry behind")
	}
}
