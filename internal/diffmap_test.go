package internal

import "testing"

func TestDiffMap_CommentableLines(t *testing.T) {
	diff := `diff --git a/pkg/x.go b/pkg/x.go
--- a/pkg/x.go
+++ b/pkg/x.go
@@ -10,3 +10,4 @@ func f() {
 context1
-removed
+added1
+added2
 context2`
	dm := BuildDiffMap(diff)
	// new-side numbering starts at 10: context1=10, added1=11, added2=12, context2=13.
	for _, ln := range []int{10, 11, 12, 13} {
		if !dm.Commentable("pkg/x.go", ln) {
			t.Errorf("line %d should be commentable", ln)
		}
	}
	if dm.Commentable("pkg/x.go", 99) {
		t.Error("line outside the hunk must not be commentable")
	}
	if dm.Commentable("other.go", 10) {
		t.Error("untouched file must not be commentable")
	}
}

func TestDiffMap_NewFileOnly(t *testing.T) {
	diff := `diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1,2 @@
+package new
+// hi`
	dm := BuildDiffMap(diff)
	if !dm.Commentable("new.go", 1) || !dm.Commentable("new.go", 2) {
		t.Error("added lines in a new file should be commentable")
	}
}
