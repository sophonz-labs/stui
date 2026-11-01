package orch

import "testing"

func TestMayEdit(t *testing.T) {
	root := "/work/app"
	owns := ParseOwns("src/auth/**, docs/auth.md ,web/*.ts,lib")
	for path, want := range map[string]bool{
		"/work/app/src/auth/login.go":      true,
		"/work/app/src/auth/deep/x/y.go":   true,
		"/work/app/docs/auth.md":           true,
		"/work/app/web/a.ts":               true,
		"/work/app/web/sub/a.ts":           false, // * stops at /
		"/work/app/lib/util/x.go":          true,  // a directory owns what is under it
		"/work/app/src/billing/pay.go":     false,
		"/work/app/docs/auth.md.bak":       false,
		"src/auth/relative.go":             true, // relative to the folder
		"/tmp/scratch.txt":                 true, // outside the folder: not held back
		"/work/app-other/src/billing/x.go": true, // a sibling folder is outside too
	} {
		if ok, why := MayEdit(owns, root, path); ok != want {
			t.Errorf("%s: %v (%s)", path, ok, why)
		}
	}
	if ok, why := MayEdit(owns, root, "/work/app/src/billing/pay.go"); ok || why == "" {
		t.Fatal("a refusal says why")
	}
	if ok, _ := MayEdit(nil, root, "/work/app/anything"); !ok {
		t.Fatal("no patterns: anything")
	}
	if ok, _ := MayEdit(ParseOwns("**/*_test.go"), root, "/work/app/a_test.go"); !ok {
		t.Fatal("**/ matches no directory too")
	}
}
