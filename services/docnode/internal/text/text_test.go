package text

import "testing"

func apply(t *testing.T, doc string, changes ...Change) string {
	t.Helper()
	out, err := Apply(Encode(doc), changes)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return Decode(out)
}

func TestInsertDeleteReplace(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		changes []Change
		want    string
	}{
		{"insert", "hello", []Change{{5, 5, " world"}}, "hello world"},
		{"delete", "hello world", []Change{{5, 11, ""}}, "hello"},
		{"replace", "hello world", []Change{{6, 11, "there"}}, "hello there"},
		{"several, positions refer to original", "abcdef",
			[]Change{{0, 1, "X"}, {2, 2, "-"}, {4, 6, ""}}, "Xb-cd"},
		{"two inserts at same point keep order", "ab", []Change{{1, 1, "1"}, {1, 1, "2"}}, "a12b"},
		{"empty change list", "same", nil, "same"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := apply(t, c.doc, c.changes...); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestPositionsAreUTF16(t *testing.T) {
	doc := "🎉 party" // the emoji is 2 UTF-16 units
	if Len(doc) != 8 {
		t.Fatalf("Len = %d, want 8", Len(doc))
	}
	if got := apply(t, doc, Change{3, 3, "big "}); got != "🎉 big party" {
		t.Errorf("got %q", got)
	}
	if got := apply(t, "नमस्ते", Change{6, 6, "!"}); got != "नमस्ते!" {
		t.Errorf("got %q", got)
	}
}

func TestInvalidChangesAreRejected(t *testing.T) {
	doc := Encode("🎉 abc")
	bad := map[string][]Change{
		"past end":         {{0, 99, ""}},
		"negative":         {{-1, 0, ""}},
		"reversed":         {{3, 2, ""}},
		"overlapping":      {{0, 4, ""}, {3, 5, ""}},
		"unsorted":         {{4, 4, "x"}, {1, 1, "y"}},
		"splits emoji":     {{1, 1, "x"}},
		"ends inside pair": {{0, 1, ""}},
	}
	for name, changes := range bad {
		if _, err := Apply(doc, changes); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestApplyDoesNotModifyInput(t *testing.T) {
	doc := Encode("abc")
	if _, err := Apply(doc, []Change{{0, 3, "xyz"}}); err != nil {
		t.Fatal(err)
	}
	if Decode(doc) != "abc" {
		t.Errorf("input was modified: %q", Decode(doc))
	}
}