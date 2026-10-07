package channel

import "testing"

func TestRegistryRejectsDuplicateAndListsSorted(t *testing.T) {
	first := Descriptor{ID: "test-a", Name: "A", Category: SMS}
	second := Descriptor{ID: "test-b", Name: "B", Category: SMS}
	if err := Register(first); err != nil {
		t.Fatal(err)
	}
	if err := Register(first); err == nil {
		t.Fatal("duplicate descriptor accepted")
	}
	if err := Register(second); err != nil {
		t.Fatal(err)
	}
	items := List(SMS)
	if len(items) != 2 || items[0].ID != "test-a" || items[1].ID != "test-b" {
		t.Fatalf("unexpected catalogue: %#v", items)
	}
}
