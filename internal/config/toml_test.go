package config

import (
	"reflect"
	"testing"
)

func TestParseEmpty(t *testing.T) {
	m, err := Parse("")
	if err != nil {
		t.Fatalf("Parse(\"\"): %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("expected empty map, got %v", m)
	}
}

func TestParseCommentsAndBlanks(t *testing.T) {
	in := `# top comment

# another
key = "val" # trailing comment
`
	m, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := m["key"]; got != "val" {
		t.Fatalf("key = %v, want val", got)
	}
	if len(m) != 1 {
		t.Fatalf("expected 1 key, got %d: %v", len(m), m)
	}
}

func TestParseStringsEscapes(t *testing.T) {
	in := `s = "hello\nworld\t\"quoted\"\\back"`
	m, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := "hello\nworld\t\"quoted\"\\back"
	if got := m["s"]; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestParseInts(t *testing.T) {
	cases := map[string]int{
		"x = 42":        42,
		"y = -7":        -7,
		"z = +3":        3,
		"n = 1_000_000": 1000000,
	}
	for in, want := range cases {
		m, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		// extract the single key
		for _, v := range m {
			if got, ok := v.(int); !ok || got != want {
				t.Fatalf("Parse(%q): got %v (%T), want %d", in, v, v, want)
			}
		}
	}
}

func TestParseBools(t *testing.T) {
	m, err := Parse("a = true\nb = false")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m["a"] != true {
		t.Fatalf("a = %v, want true", m["a"])
	}
	if m["b"] != false {
		t.Fatalf("b = %v, want false", m["b"])
	}
}

func TestParseArrays(t *testing.T) {
	in := `empty = []
nums = [1, 2, 3]
mix = ["a", 1, true, -5]
trail = [1, 2,]`
	m, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := m["empty"]; !reflect.DeepEqual(got, []interface{}{}) {
		t.Fatalf("empty = %v", got)
	}
	if got := m["nums"]; !reflect.DeepEqual(got, []interface{}{1, 2, 3}) {
		t.Fatalf("nums = %v", got)
	}
	wantMix := []interface{}{"a", 1, true, -5}
	if got := m["mix"]; !reflect.DeepEqual(got, wantMix) {
		t.Fatalf("mix = %v, want %v", got, wantMix)
	}
	if got := m["trail"]; !reflect.DeepEqual(got, []interface{}{1, 2}) {
		t.Fatalf("trail = %v", got)
	}
}

func TestParseNestedTables(t *testing.T) {
	in := `[a]
x = 1
[a.b]
y = 2
[a.b.c]
z = "deep"
[a.other]
w = true
`
	m, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	a, ok := m["a"].(map[string]interface{})
	if !ok {
		t.Fatalf("a not a table: %T", m["a"])
	}
	if a["x"] != 1 {
		t.Fatalf("a.x = %v", a["x"])
	}
	ab, ok := a["b"].(map[string]interface{})
	if !ok {
		t.Fatalf("a.b not a table: %T", a["b"])
	}
	if ab["y"] != 2 {
		t.Fatalf("a.b.y = %v", ab["y"])
	}
	abc, ok := ab["c"].(map[string]interface{})
	if !ok {
		t.Fatalf("a.b.c not a table")
	}
	if abc["z"] != "deep" {
		t.Fatalf("a.b.c.z = %v", abc["z"])
	}
	aoth, ok := a["other"].(map[string]interface{})
	if !ok {
		t.Fatalf("a.other not a table")
	}
	if aoth["w"] != true {
		t.Fatalf("a.other.w = %v", aoth["w"])
	}
}

func TestParseSectionWithSubsectionDot(t *testing.T) {
	in := `[pricing.anthropic.claude-sonnet-4-20250514]
input = 3.0
output = 15.0
cache_read = 0.3
cache_write = 3.75
`
	m, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	pr, ok := m["pricing"].(map[string]interface{})
	if !ok {
		t.Fatal("pricing not a table")
	}
	ant, ok := pr["anthropic"].(map[string]interface{})
	if !ok {
		t.Fatal("pricing.anthropic not a table")
	}
	mdl, ok := ant["claude-sonnet-4-20250514"].(map[string]interface{})
	if !ok {
		t.Fatal("pricing.anthropic.claude-sonnet-4-20250514 not a table")
	}
	if mdl["input"] != 3.0 {
		t.Fatalf("input = %v (%T), want 3.0", mdl["input"], mdl["input"])
	}
	if mdl["output"] != 15.0 {
		t.Fatalf("output = %v, want 15.0", mdl["output"])
	}
	if mdl["cache_read"] != 0.3 {
		t.Fatalf("cache_read = %v, want 0.3", mdl["cache_read"])
	}
	if mdl["cache_write"] != 3.75 {
		t.Fatalf("cache_write = %v, want 3.75", mdl["cache_write"])
	}
}

func TestParseErrors(t *testing.T) {
	bad := []string{
		"key = ",             // empty value
		"= 1",                // empty key
		"[unclosed",          // unclosed header
		`[a]` + "\n" + `x =`, // empty value after header
		"s = \"unterminated", // unterminated string
		"bad = @@",           // invalid value
		"key no eq 1",        // no equals
	}
	for _, in := range bad {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q): expected error, got nil", in)
		}
	}
}

func TestParseDuplicateKey(t *testing.T) {
	in := `x = 1
x = 2
`
	if _, err := Parse(in); err == nil {
		t.Fatal("expected duplicate key error")
	}
}

func TestParseCommentInsideString(t *testing.T) {
	in := `s = "a # b" # real comment`
	m, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m["s"] != "a # b" {
		t.Fatalf("got %v", m["s"])
	}
}

func TestParseArrayWithStringsContainingCommas(t *testing.T) {
	in := `x = ["a,b", "c", "d,e,f"]`
	m, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []interface{}{"a,b", "c", "d,e,f"}
	if got := m["x"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
